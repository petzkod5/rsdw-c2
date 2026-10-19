package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Regression tests for the dashboard status going stale or reading "Check
// failed" (#62). They depend only on fixtureApp, collectTelemetry and
// telemetryFor, so they can also be run against the single-pass collector they
// were written to replace.

// scriptedRunner delays or fails chosen kubectl calls. A delay ends early,
// with the context error, when the caller's timeout fires, like a real exec.
type scriptedRunner struct {
	base  CommandRunner
	delay func(call string) time.Duration
	fail  func(call string) error
}

func (r *scriptedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	if r.fail != nil {
		if err := r.fail(call); err != nil {
			return nil, err
		}
	}
	if r.delay != nil {
		if d := r.delay(call); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return r.base.Run(ctx, name, args...)
}

func serverStatusNow(app *App, server Server) Telemetry {
	return app.telemetryFor(app.store.Snapshot().Servers[server.ID], "1h")
}

// A slow-but-answering health probe plus metric sources that all fail must
// leave the server ONLINE, with the metric readings unavailable and explained.
func TestHealthyServerStaysOnlineWhenMetricSourcesFailOrTimeOut(t *testing.T) {
	app, runner, server := fixtureApp(t)
	k := app.orchestrator.(*kubeOrchestrator)
	shortenMetricTimeouts(k)
	k.runner = &scriptedRunner{
		base: runner,
		delay: func(call string) time.Duration {
			switch {
			case strings.Contains(call, "/api/health"):
				// Answers, but slower than the old fixed 1.5 s command timeout.
				return 1600 * time.Millisecond
			case strings.Contains(call, "/api/players"), strings.Contains(call, "df -Pk"), strings.Contains(call, "cat /proc/net/dev"), strings.Contains(call, "/api/metrics"):
				return time.Hour
			}
			return 0
		},
		fail: func(call string) error {
			if strings.Contains(call, "get --raw") {
				return errors.New("metrics.k8s.io unavailable")
			}
			return nil
		},
	}
	for pass := 1; pass <= 2; pass++ {
		app.collectTelemetry(context.Background())
		got := serverStatusNow(app, server)
		if got.Server.Status != StatusOnline {
			t.Fatalf("pass %d: status = %q, want online; readings: engineReady=%+v players=%+v", pass, got.Server.Status, got.Metrics["engineReady"], got.Metrics["players"])
		}
		expectMetric(t, got.Metrics, "engineReady", "available", number(1))
		for _, key := range []string{"players", "diskPercent", "networkBytesPerSecond", "cpuCores"} {
			reading := got.Metrics[key]
			if reading.Value != nil || reading.Status == "available" || reading.Reason == "" {
				t.Fatalf("pass %d: %s = %+v, want unavailable with a reason", pass, key, reading)
			}
		}
		if reading, ok := got.Metrics["tickRate"]; ok && (reading.Value != nil || reading.Reason == "") {
			t.Fatalf("pass %d: tickRate = %+v, want unavailable with a reason", pass, reading)
		}
	}
}

// One failed health probe is a hiccup; the second consecutive one is an outage.
func TestOneFailedHealthProbeKeepsStatusAndTwoChangeIt(t *testing.T) {
	app, runner, server := fixtureApp(t)
	failing := false
	runner.override = func(call string) ([]byte, error, bool) {
		if failing && strings.Contains(call, "/api/health") {
			return nil, errors.New("exec timed out"), true
		}
		return nil, nil, false
	}
	for _, step := range []struct {
		name    string
		failing bool
		want    Status
	}{
		{"healthy", false, StatusOnline},
		{"first failure", true, StatusOnline},
		{"second failure", true, StatusAttention},
		{"third failure", true, StatusAttention},
		{"recovered", false, StatusOnline},
		{"failure after recovery", true, StatusOnline},
	} {
		failing = step.failing
		app.collectTelemetry(context.Background())
		got := serverStatusNow(app, server)
		if got.Server.Status != step.want {
			t.Fatalf("%s: status = %q, want %q (engineReady %+v)", step.name, got.Server.Status, step.want, got.Metrics["engineReady"])
		}
		if step.failing && got.Metrics["engineReady"].Reason == "" {
			t.Fatalf("%s: the failed probe left no reason on engineReady", step.name)
		}
	}
}

// A failed Pod re-check is not evidence about the server: the last known status
// stays and the earlier readings are not wiped.
func TestFailedPodRecheckKeepsLastKnownStatus(t *testing.T) {
	app, runner, server := fixtureApp(t)
	app.collectTelemetry(context.Background())
	before := serverStatusNow(app, server)
	if before.Server.Status != StatusOnline {
		t.Fatalf("setup status = %q", before.Server.Status)
	}
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "get pod world-pod") {
			return nil, errors.New("apiserver unavailable"), true
		}
		return nil, nil, false
	}
	app.collectTelemetry(context.Background())
	got := serverStatusNow(app, server)
	if got.Server.Status != StatusOnline {
		t.Fatalf("status after failed re-check = %q, want online (lastSeen %q)", got.Server.Status, got.Server.LastSeen)
	}
	if got.Server.LastSeen != before.Server.LastSeen {
		t.Fatalf("a failed re-check advanced lastSeen: %q -> %q", before.Server.LastSeen, got.Server.LastSeen)
	}
	expectMetric(t, got.Metrics, "engineReady", "available", number(1))
	expectMetric(t, got.Metrics, "cpuLimitCores", "available", number(.5))
}

const loadBalancerService = `{"spec":{"type":"LoadBalancer","ports":[{"name":"game","port":7777,"nodePort":30100}]},"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.42"}]}}}`

// The join address comes from the liveness tier: after a restart of the console
// an ONLINE server has its endpoint after the first status pass, before any
// metrics pass runs, and a failed Service read is retried on the next pass.
func TestLivenessPassResolvesAndCarriesTheEndpoint(t *testing.T) {
	app, runner, server := fixtureApp(t)
	serviceReads, serviceUp := 0, false
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "get service") {
			serviceReads++
			if !serviceUp {
				return nil, errors.New("services is forbidden"), true
			}
			return []byte(loadBalancerService), nil, true
		}
		return nil, nil, false
	}
	app.collectLiveness(context.Background())
	got := serverStatusNow(app, server)
	if got.Server.Status != StatusOnline || got.Server.Endpoint != "" || serviceReads != 1 {
		t.Fatalf("first pass: status %q endpoint %q reads %d", got.Server.Status, got.Server.Endpoint, serviceReads)
	}
	serviceUp = true
	app.collectLiveness(context.Background())
	if got = serverStatusNow(app, server); got.Server.Endpoint != "203.0.113.42:7777" || serviceReads != 2 {
		t.Fatalf("retry: endpoint %q reads %d, want the address after one more read", got.Server.Endpoint, serviceReads)
	}
	// A known address is not read again until the metrics interval has passed,
	// and survives a metrics pass and a failing Service read.
	app.collectLiveness(context.Background())
	app.collectMetrics(context.Background(), true)
	if got = serverStatusNow(app, server); got.Server.Endpoint != "203.0.113.42:7777" || serviceReads != 2 {
		t.Fatalf("carried: endpoint %q reads %d, want no extra read", got.Server.Endpoint, serviceReads)
	}
	serviceUp = false
	cache := app.observations()
	cache.mu.Lock()
	history := cache.history[server.ID]
	history[len(history)-1].endpointAt = time.Now().Add(-2 * time.Minute)
	cache.mu.Unlock()
	app.collectLiveness(context.Background())
	if got = serverStatusNow(app, server); got.Server.Endpoint != "203.0.113.42:7777" || serviceReads != 3 {
		t.Fatalf("refresh failure: endpoint %q reads %d, want the last known address", got.Server.Endpoint, serviceReads)
	}
}

// One failed probe keeps the status, and the uptime it was last confirmed with.
func TestFailedHealthProbeCarriesUptimeWhileOnlineIsRetained(t *testing.T) {
	app, runner, server := fixtureApp(t)
	failing := false
	runner.override = func(call string) ([]byte, error, bool) {
		if failing && strings.Contains(call, "/api/health") {
			return nil, errors.New("exec timed out"), true
		}
		return nil, nil, false
	}
	app.collectTelemetry(context.Background())
	failing = true
	app.collectLiveness(context.Background())
	got := serverStatusNow(app, server)
	if got.Server.Status != StatusOnline || got.Server.UptimeSeconds != 120 {
		t.Fatalf("after one failed probe: status %q uptime %d, want online with 120", got.Server.Status, got.Server.UptimeSeconds)
	}
	expectMetric(t, got.Metrics, "uptimeSeconds", "available", number(120))
	if got.Metrics["engineReady"].Value != nil || got.Metrics["engineReady"].Reason == "" {
		t.Fatalf("engineReady = %+v, want the failure reason on the Telemetry page", got.Metrics["engineReady"])
	}
	app.collectLiveness(context.Background())
	got = serverStatusNow(app, server)
	if got.Server.Status != StatusAttention || got.Server.UptimeSeconds != 0 {
		t.Fatalf("after two failed probes: status %q uptime %d, want attention without an uptime", got.Server.Status, got.Server.UptimeSeconds)
	}
}

// The roster response carries the whole freshness window, so every role can
// validate freshForMs without the admin-only polling configuration.
func TestRosterFreshnessWindowReachesViewers(t *testing.T) {
	app, _, server := fixtureApp(t)
	app.collectTelemetry(context.Background())
	got := serverStatusNow(app, server)
	if got.PlayerRoster.Status != RosterAvailable || got.PlayerRoster.FreshWindowMs != 180000 || got.PlayerRoster.FreshForMs <= 0 || got.PlayerRoster.FreshForMs > 180000 {
		t.Fatalf("roster freshness = %+v, want a 180000 ms window (3 x the 60s metrics interval)", got.PlayerRoster)
	}
	viewer := viewerTelemetry(got)
	if viewer.PlayerRoster.FreshWindowMs != 180000 || viewer.PlayerRoster.FreshForMs != got.PlayerRoster.FreshForMs {
		t.Fatalf("viewer roster freshness = %+v", viewer.PlayerRoster)
	}
}
