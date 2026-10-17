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
