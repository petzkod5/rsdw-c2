package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func countCalls(runner *telemetryRunner, needle string) int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	count := 0
	for _, call := range runner.calls {
		if strings.Contains(call, needle) {
			count++
		}
	}
	return count
}

func setTelemetryConfig(app *App, cfg TelemetryConfig) {
	app.telemetryCfg = cfg
	app.orchestrator.(*kubeOrchestrator).telemetry = cfg
}

func latestObservation(t *testing.T, app *App, id string) observation {
	t.Helper()
	cache := app.observations()
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	history := cache.history[id]
	if len(history) == 0 {
		t.Fatal("no observation recorded")
	}
	return history[len(history)-1]
}

func TestTelemetryConfigDefaultsAndBounds(t *testing.T) {
	got, err := telemetryConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := defaultTelemetryConfig()
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if got.LivenessInterval != 15*time.Second || got.MetricsInterval != time.Minute || got.Disk.Interval != 5*time.Minute || !got.ExecMetrics {
		t.Fatalf("unexpected defaults %+v", got)
	}
	for name, value := range map[string]string{
		"RSDW_TELEMETRY_LIVENESS_INTERVAL":    "1s",
		"RSDW_TELEMETRY_METRICS_INTERVAL":     "1h",
		"RSDW_TELEMETRY_DISK_INTERVAL":        "10s",
		"RSDW_TELEMETRY_NETWORK_INTERVAL":     "soon",
		"RSDW_TELEMETRY_TICK_INTERVAL":        "-5s",
		"RSDW_TELEMETRY_KUBE_TIMEOUT":         "0s",
		"RSDW_TELEMETRY_HEALTH_TIMEOUT":       "2m",
		"RSDW_TELEMETRY_EXEC_TIMEOUT":         "fast",
		"RSDW_TELEMETRY_EXEC_METRICS_ENABLED": "maybe",
		"RSDW_TELEMETRY_RESOURCES_ENABLED":    "2",
		"RSDW_TELEMETRY_PLAYERS_ENABLED":      "yes",
		"RSDW_TELEMETRY_NETWORK_ENABLED":      "n",
		"RSDW_TELEMETRY_DISK_ENABLED":         "disabled",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := telemetryConfigFromEnv(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s=%q error = %v, want one naming the variable", name, value, err)
			}
		})
	}
}

func TestTelemetryConfigOverrides(t *testing.T) {
	for name, value := range map[string]string{
		"RSDW_TELEMETRY_LIVENESS_INTERVAL":    "30s",
		"RSDW_TELEMETRY_METRICS_INTERVAL":     "2m",
		"RSDW_TELEMETRY_DISK_INTERVAL":        "10m",
		"RSDW_TELEMETRY_EXEC_METRICS_ENABLED": "false",
		"RSDW_TELEMETRY_PLAYERS_ENABLED":      "false",
		"RSDW_TELEMETRY_KUBE_TIMEOUT":         "7s",
	} {
		t.Setenv(name, value)
	}
	got, err := telemetryConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got.LivenessInterval != 30*time.Second || got.MetricsInterval != 2*time.Minute || got.Disk.Interval != 10*time.Minute || got.KubeTimeout != 7*time.Second {
		t.Fatalf("overrides = %+v", got)
	}
	// Tick and network follow the metrics interval unless set on their own.
	if got.Tick.Interval != 2*time.Minute || got.Network.Interval != 2*time.Minute || got.Resources.Interval != 2*time.Minute {
		t.Fatalf("source intervals = %+v", got)
	}
	if got.sourceActive(sourcePlayers) || got.sourceActive(sourceDisk) || got.sourceActive(sourceNetwork) || got.sourceActive(sourceTick) || !got.sourceActive(sourceResources) {
		t.Fatalf("exec master switch did not turn off exec sources: %+v", got)
	}
}

func TestTelemetryConfigRejectsMemoryPressureWithoutResources(t *testing.T) {
	cfg := defaultTelemetryConfig()
	cfg.Resources.Enabled = false
	if err := cfg.validateWith(MemoryPressurePolicy{Enabled: true}); err == nil || !strings.Contains(err.Error(), "RSDW_TELEMETRY_RESOURCES_ENABLED") {
		t.Fatalf("error = %v", err)
	}
	if err := cfg.validateWith(MemoryPressurePolicy{}); err != nil {
		t.Fatal(err)
	}
	if err := defaultTelemetryConfig().validateWith(MemoryPressurePolicy{Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestTelemetryConfigViewReportsEffectiveValues(t *testing.T) {
	cfg := defaultTelemetryConfig()
	cfg.ExecMetrics = false
	view := cfg.view()
	if view.LivenessIntervalSeconds != 15 || view.MetricsIntervalSeconds != 60 || view.ExecMetricsEnabled || view.Sources["disk"].Enabled || !view.Sources["resources"].Enabled || view.Sources["disk"].IntervalSeconds != 300 {
		t.Fatalf("view = %+v", view)
	}
	if view.EstimatedExecsPerMinute != 4 {
		t.Fatalf("liveness-only exec rate = %v, want 4 (one health probe every 15s)", view.EstimatedExecsPerMinute)
	}
	// 4 health probes + players and network every 60s + disk every 300s, plus
	// tick every 60s once tick metrics are opted in.
	t.Setenv("RSDW_TICK_METRICS", "false")
	if got := defaultTelemetryConfig().view().EstimatedExecsPerMinute; got != 4+2+0.2 {
		t.Fatalf("default exec rate = %v", got)
	}
	t.Setenv("RSDW_TICK_METRICS", "true")
	if got := defaultTelemetryConfig().view(); got.EstimatedExecsPerMinute != 4+3+0.2 || !got.Sources["tick"].Enabled {
		t.Fatalf("exec rate with tick metrics = %v, tick = %+v", got.EstimatedExecsPerMinute, got.Sources["tick"])
	}
	if view.LivenessFreshnessSeconds != 45 || view.MetricFreshnessSeconds["players"] != 180 || view.MetricFreshnessSeconds["disk"] != 900 {
		t.Fatalf("freshness = %+v", view)
	}
}

func TestBootstrapExposesTelemetryConfigToAdminsOnly(t *testing.T) {
	app := newTestApp(t, true)
	app.telemetryCfg = defaultTelemetryConfig()
	app.telemetryCfg.LivenessInterval = 20 * time.Second
	res := requestJSON(t, app, http.MethodGet, "/api/bootstrap", "")
	var body struct {
		TelemetryConfig *TelemetryConfigView `json:"telemetryConfig"`
	}
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.TelemetryConfig == nil {
		t.Fatalf("bootstrap = %d %s", res.Code, res.Body.String())
	}
	if body.TelemetryConfig.LivenessIntervalSeconds != 20 || !body.TelemetryConfig.ExecMetricsEnabled || body.TelemetryConfig.Sources["tick"].IntervalSeconds != 60 {
		t.Fatalf("telemetryConfig = %+v", body.TelemetryConfig)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{Subject: "viewer", Role: RoleViewer}))
	rec := httptest.NewRecorder()
	app.handleBootstrap(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "telemetryConfig") {
		t.Fatalf("viewer bootstrap leaked the polling configuration: %s", rec.Body.String())
	}
}

// Liveness and each metric group are judged against their own window.
func TestFreshnessIsPerTier(t *testing.T) {
	cfg := defaultTelemetryConfig()
	now := time.Now().UTC()
	build := func(livenessAge, playersAge, diskAge time.Duration) observation {
		metrics := emptyMetrics()
		setReading(metrics, "players", 3, now.Add(-playersAge))
		setReading(metrics, "diskPercent", 40, now.Add(-diskAge))
		stampWindow(metrics, sourcePlayers.keys(), cfg.metricWindow(sourcePlayers))
		stampWindow(metrics, sourceDisk.keys(), cfg.metricWindow(sourceDisk))
		return observation{at: now.Add(-livenessAge), status: StatusOnline, metrics: metrics, maxAge: cfg.livenessWindow()}
	}
	for _, tc := range []struct {
		name                      string
		liveness, players, disk   time.Duration
		status                    Status
		playersStatus, diskStatus string
	}{
		{"all fresh", 10 * time.Second, 20 * time.Second, 30 * time.Second, StatusOnline, "available", "available"},
		{"metrics older than the liveness window stay fresh", 10 * time.Second, 170 * time.Second, 10 * time.Minute, StatusOnline, "available", "available"},
		{"stale metrics never touch status", 10 * time.Second, 200 * time.Second, 20 * time.Minute, StatusOnline, "stale", "stale"},
		{"liveness expires on its own window", 50 * time.Second, 20 * time.Second, 30 * time.Second, StatusUnknown, "available", "available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := joinObservation(Server{ID: "world"}, build(tc.liveness, tc.players, tc.disk), now)
			if got.Status != tc.status || got.Metrics["players"].Status != tc.playersStatus || got.Metrics["diskPercent"].Status != tc.diskStatus {
				t.Fatalf("status=%s players=%s disk=%s, want %s %s %s", got.Status, got.Metrics["players"].Status, got.Metrics["diskPercent"].Status, tc.status, tc.playersStatus, tc.diskStatus)
			}
		})
	}
	// An observation without its own window keeps the 45 second default.
	legacy := observation{at: now.Add(-50 * time.Second), status: StatusOnline, metrics: emptyMetrics()}
	if got := joinObservation(Server{ID: "world"}, legacy, now); got.Status != StatusUnknown {
		t.Fatalf("legacy status = %s", got.Status)
	}
}

func TestDueSourcesFollowTheirIntervals(t *testing.T) {
	cfg := defaultTelemetryConfig()
	start := time.Now()
	due := map[metricSource]time.Time{}
	first := cfg.dueSources(due, start, false)
	for _, s := range append(slices.Clone(metricSources), sourceEndpoint) {
		if !first[s] {
			t.Fatalf("%s not due on the first pass", s)
		}
		due[s] = start.Add(cfg.sourceInterval(s))
	}
	if got := cfg.dueSources(due, start.Add(10*time.Second), false); len(got) != 0 {
		t.Fatalf("due after 10s: %v", got)
	}
	minute := cfg.dueSources(due, start.Add(time.Minute), false)
	for _, s := range []metricSource{sourceResources, sourcePlayers, sourceNetwork, sourceTick, sourceEndpoint} {
		if !minute[s] {
			t.Fatalf("%s not due after 60s", s)
		}
	}
	if minute[sourceDisk] {
		t.Fatal("disk is due after 60s; it runs every 5 minutes")
	}
	if cfg.dueSources(due, start.Add(200*time.Second), false)[sourceDisk] {
		t.Fatal("disk due after 200s")
	}
	if !cfg.dueSources(due, start.Add(5*time.Minute), false)[sourceDisk] {
		t.Fatal("disk not due after 5 minutes")
	}
	cfg.Disk.Enabled = false
	cfg.ExecMetrics = true
	if got := cfg.dueSources(map[metricSource]time.Time{}, start, true); got[sourceDisk] || !got[sourcePlayers] {
		t.Fatalf("forced pass = %v", got)
	}
}

// With exec metrics off the only process started in the game container is the
// health probe, and nothing that reads the metric cache misbehaves.
func TestExecMetricsDisabledRunsOnlyTheHealthProbe(t *testing.T) {
	app, runner, server := fixtureApp(t)
	cfg := defaultTelemetryConfig()
	cfg.ExecMetrics = false
	setTelemetryConfig(app, cfg)
	if err := app.store.Update(func(state *State) error {
		integration := integrationFixture()
		integration.ServerIDs = []string{server.ID}
		state.Integrations[integration.ID] = integration
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		app.collectTelemetry(context.Background())
	}
	runner.mu.Lock()
	for _, call := range runner.calls {
		if strings.Contains(call, " exec ") && !strings.Contains(call, "/api/health") {
			t.Errorf("exec metrics are off but ran %s", call)
		}
	}
	runner.mu.Unlock()
	if got := countCalls(runner, "/api/health"); got != 3 {
		t.Fatalf("health probes = %d, want one per pass", got)
	}
	got := serverStatusNow(app, server)
	if got.Server.Status != StatusOnline {
		t.Fatalf("status = %q", got.Server.Status)
	}
	for _, key := range append(slices.Clone(networkKeys), append(slices.Clone(diskKeys), "players")...) {
		reading := got.Metrics[key]
		if reading.Status != "disabled" || reading.Value != nil || !strings.Contains(reading.Reason, "Turned off by configuration") {
			t.Fatalf("%s = %+v, want disabled", key, reading)
		}
	}
	// The inputs of memory pressure and server alerts still arrive.
	expectMetric(t, got.Metrics, "memoryUsedBytes", "available", number(67108864))
	expectMetric(t, got.Metrics, "memoryLimitBytes", "available", number(2147483648))
	expectMetric(t, got.Metrics, "engineReady", "available", number(1))
	if !got.Server.MetricsAvailable {
		t.Fatal("metricsAvailable is false although resources are collected")
	}
	if state := app.store.Snapshot(); len(state.Events) != 0 {
		t.Fatalf("disabled sources produced events: %v", eventKinds(&state))
	}
}

func TestMetricSourcesAreIndividuallyToggleable(t *testing.T) {
	for _, tc := range []struct {
		source   metricSource
		command  string
		disabled string
		other    string
	}{
		{sourceResources, "get --raw", "cpuCores", "players"},
		{sourcePlayers, "/api/players", "players", "diskPercent"},
		{sourceNetwork, "cat /proc/net/dev", "networkBytesPerSecond", "players"},
		{sourceDisk, "df -Pk", "diskPercent", "players"},
		{sourceTick, "/api/metrics", "tickRate", "players"},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			app, runner, server := fixtureApp(t)
			cfg := defaultTelemetryConfig()
			switch tc.source {
			case sourceResources:
				cfg.Resources.Enabled = false
			case sourcePlayers:
				cfg.Players.Enabled = false
			case sourceNetwork:
				cfg.Network.Enabled = false
			case sourceDisk:
				cfg.Disk.Enabled = false
			case sourceTick:
				cfg.Tick.Enabled = false
			}
			setTelemetryConfig(app, cfg)
			app.collectTelemetry(context.Background())
			if got := countCalls(runner, tc.command); got != 0 {
				t.Fatalf("%s ran %d times while disabled", tc.command, got)
			}
			got := serverStatusNow(app, server)
			if reading, ok := got.Metrics[tc.disabled]; ok && (reading.Status != "disabled" || reading.Value != nil) {
				t.Fatalf("%s = %+v, want disabled", tc.disabled, reading)
			}
			expectMetric(t, got.Metrics, tc.other, "available", got.Metrics[tc.other].Value)
			if got.Server.Status != StatusOnline {
				t.Fatalf("status = %q", got.Server.Status)
			}
		})
	}
}

// Readings written by the metrics tier carry forward, with their own
// timestamps, through liveness observations until the container changes.
func TestLivenessObservationsCarryMetricsAndResetOnNewContainer(t *testing.T) {
	app, runner, server := fixtureApp(t)
	app.collectTelemetry(context.Background())
	select {
	case <-app.observations().kick: // the first container observation wakes the metrics loop
	default:
		t.Fatal("the first container observation did not wake the metrics loop")
	}
	first := latestObservation(t, app, server.ID)
	players := first.metrics["players"]
	if players.Status != "available" || first.network == nil {
		t.Fatalf("setup players=%+v network=%v", players, first.network)
	}
	app.collectLiveness(context.Background())
	second := latestObservation(t, app, server.ID)
	if !second.at.After(first.at) {
		t.Fatal("liveness pass did not record an observation")
	}
	carried := second.metrics["players"]
	if carried.Status != "available" || carried.ObservedAt == nil || !carried.ObservedAt.Equal(*players.ObservedAt) || second.network == nil {
		t.Fatalf("carried players = %+v network=%v", carried, second.network)
	}
	if second.playerRoster.Status != RosterAvailable {
		t.Fatalf("roster = %+v", second.playerRoster)
	}
	if got := len(app.observations().history[server.ID]); got != 2 {
		t.Fatalf("history length = %d", got)
	}
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "get pods") || strings.Contains(call, "get pod world-pod") {
			pod := strings.ReplaceAll(fixturePod(), "container-id", "new-container")
			if strings.Contains(call, "get pods") {
				pod = `{"items":[` + pod + `]}`
			}
			return []byte(pod), nil, true
		}
		return nil, nil, false
	}
	app.collectLiveness(context.Background())
	third := latestObservation(t, app, server.ID)
	reset := third.metrics["players"]
	if third.runtime == second.runtime || reset.Status != "warming_up" || reset.Value != nil || third.network != nil || third.playerRoster.Status != RosterUnavailable {
		t.Fatalf("new container kept old readings: runtime=%s players=%+v", third.runtime, reset)
	}
	expectMetric(t, third.metrics, "engineReady", "available", number(1))
	select {
	case <-app.observations().kick:
	default:
		t.Fatal("a new container did not wake the metrics loop")
	}
}

// A metrics pass that commits while a liveness pass is in flight must not be
// lost when the liveness observation is recorded.
func TestMetricsCommittedDuringLivenessPassSurvives(t *testing.T) {
	app, runner, server := fixtureApp(t)
	app.collectTelemetry(context.Background())
	cache := app.observations()
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "/api/health") {
			// The metrics pass lands after the liveness pass read its baseline.
			cache.mu.Lock()
			history := cache.history[server.ID]
			last := history[len(history)-1]
			reading := last.metrics["players"]
			value, at := 7.0, time.Now().UTC()
			reading.Value, reading.ObservedAt = &value, &at
			history[len(history)-1] = patchObservation(last, metricsResult{metrics: map[string]MetricReading{"players": reading}, at: at})
			cache.mu.Unlock()
		}
		return nil, nil, false
	}
	app.collectLiveness(context.Background())
	got := latestObservation(t, app, server.ID)
	if players := got.metrics["players"]; players.Value == nil || *players.Value != 7 {
		t.Fatalf("liveness observation lost the concurrent metrics write: %+v", players)
	}
}

func TestMetricsPassNeverTouchesStatus(t *testing.T) {
	app, runner, server := fixtureApp(t)
	app.collectLiveness(context.Background())
	before := latestObservation(t, app, server.ID)
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "exec ") && !strings.Contains(call, "/api/health") || strings.Contains(call, "get --raw") {
			return nil, context.DeadlineExceeded, true
		}
		return nil, nil, false
	}
	app.collectMetrics(context.Background(), true)
	after := latestObservation(t, app, server.ID)
	if after.status != before.status || !after.at.Equal(before.at) || after.health != before.health || after.healthFailures != before.healthFailures || after.runtime != before.runtime {
		t.Fatalf("metrics pass changed liveness state: before=%+v after=%+v", before, after)
	}
	if after.metrics["players"].Status != "error" || !strings.Contains(after.metrics["players"].Reason, "deadline") {
		t.Fatalf("players = %+v", after.metrics["players"])
	}
	if after.metricsAt.IsZero() {
		t.Fatal("metrics pass left no timestamp")
	}
	expectMetric(t, after.metrics, "engineReady", "available", number(1))
}

// The metrics pass of a container that has since been replaced is discarded.
func TestMetricsResultForReplacedContainerIsDiscarded(t *testing.T) {
	app, runner, server := fixtureApp(t)
	app.collectLiveness(context.Background())
	cache := app.observations()
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "/api/players") {
			cache.mu.Lock()
			history := cache.history[server.ID]
			history[len(history)-1].runtime = "replacement/container"
			cache.mu.Unlock()
		}
		return nil, nil, false
	}
	app.collectMetrics(context.Background(), true)
	got := latestObservation(t, app, server.ID)
	if got.metrics["players"].Status == "available" || !got.metricsAt.IsZero() {
		t.Fatalf("stale pass was written: players=%+v metricsAt=%v", got.metrics["players"], got.metricsAt)
	}
	cache.mu.RLock()
	pending := len(cache.due[server.ID])
	cache.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("%d sources stayed scheduled after a discarded pass", pending)
	}
}

// A held lifecycle lock (a backup or restart in flight) must not stop status.
func TestLivenessDoesNotWaitForTheLifecycleLock(t *testing.T) {
	app, _, server := fixtureApp(t)
	app.lifecycleMu.Lock()
	app.collectLiveness(context.Background())
	app.lifecycleMu.Unlock()
	if got := serverStatusNow(app, server).Server.Status; got != StatusOnline {
		t.Fatalf("status = %q while the lifecycle lock was held", got)
	}
}

// Definitive Pod evidence moves status immediately; no probe is needed.
func TestDefinitiveKubernetesEvidenceChangesStatusAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name, needle, body string
		status             Status
		probes             int
	}{
		{"scaled to zero", "get deployment", `{"metadata":{"name":"world-rsdragonwilds","namespace":"games","uid":"deployment-uid"},"spec":{"replicas":0}}`, StatusStopped, 0},
		{"no pod", "get pods", `{"items":[]}`, StatusStarting, 0},
		{"container not ready", "get pods", strings.ReplaceAll(`{"items":[`+fixturePod()+`]}`, `"ready":true`, `"ready":false`), StatusStarting, 0},
		{"pod not ready", "get pods", strings.ReplaceAll(`{"items":[`+fixturePod()+`]}`, `"type":"Ready","status":"True"`, `"type":"Ready","status":"False"`), StatusStarting, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, runner, server := fixtureApp(t)
			app.collectLiveness(context.Background())
			if got := serverStatusNow(app, server).Server.Status; got != StatusOnline {
				t.Fatalf("setup status = %q", got)
			}
			runner.override = func(call string) ([]byte, error, bool) {
				if strings.Contains(call, tc.needle) {
					return []byte(tc.body), nil, true
				}
				return nil, nil, false
			}
			before := countCalls(runner, "/api/health")
			app.collectLiveness(context.Background())
			if got := serverStatusNow(app, server).Server.Status; got != tc.status {
				t.Fatalf("status = %q, want %q", got, tc.status)
			}
			if probes := countCalls(runner, "/api/health") - before; probes != tc.probes {
				t.Fatalf("health probes = %d, want %d", probes, tc.probes)
			}
		})
	}
}

// Inconclusive evidence keeps the last status, explains why on the Telemetry
// page, and clears once the evidence returns.
func TestInconclusiveLivenessKeepsStatusAndReportsWhy(t *testing.T) {
	app, runner, server := fixtureApp(t)
	app.collectTelemetry(context.Background())
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "get pods") {
			return nil, errors.New("connection refused"), true
		}
		return nil, nil, false
	}
	app.collectLiveness(context.Background())
	got := serverStatusNow(app, server)
	if got.Server.Status != StatusOnline {
		t.Fatalf("status = %q", got.Server.Status)
	}
	found := false
	for _, check := range got.HealthChecks {
		if check.Name == "Kubernetes status check" && check.Status == "error" && strings.Contains(check.Reason, "connection refused") && strings.Contains(check.Reason, "last known status") {
			found = true
		}
	}
	if !found {
		t.Fatalf("health checks = %+v", got.HealthChecks)
	}
	runner.override = nil
	app.collectLiveness(context.Background())
	for _, check := range serverStatusNow(app, server).HealthChecks {
		if check.Name == "Kubernetes status check" {
			t.Fatal("the failed check stayed after the evidence returned")
		}
	}
}

// The two loops run on their own timers and sources on their own intervals.
func TestRunCollectorSchedulesTiersIndependently(t *testing.T) {
	app, runner, server := fixtureApp(t)
	cfg := defaultTelemetryConfig()
	cfg.LivenessInterval = 20 * time.Millisecond
	cfg.MetricsInterval = 40 * time.Millisecond
	cfg.Players.Interval, cfg.Resources.Interval, cfg.Network.Interval, cfg.Tick.Interval = 40*time.Millisecond, 40*time.Millisecond, 40*time.Millisecond, 40*time.Millisecond
	cfg.Disk.Interval = time.Hour
	setTelemetryConfig(app, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { app.runCollector(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for countCalls(runner, "/api/players") < 3 || countCalls(runner, "/api/health") < 5 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("health=%d players=%d", countCalls(runner, "/api/health"), countCalls(runner, "/api/players"))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if got := countCalls(runner, "df -Pk"); got != 1 {
		t.Fatalf("disk ran %d times with an hour interval", got)
	}
	if got := serverStatusNow(app, server); got.Server.Status != StatusOnline || got.Metrics["players"].Status != "available" || got.Metrics["diskPercent"].Status != "available" {
		t.Fatalf("status=%q players=%+v disk=%+v", got.Server.Status, got.Metrics["players"], got.Metrics["diskPercent"])
	}
}

func TestPassesOverlapSafely(t *testing.T) {
	app, _, _ := fixtureApp(t)
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5; i++ {
			app.collectLiveness(context.Background())
			app.collectMetrics(context.Background(), true)
			calls.Add(1)
		}
	}()
	for i := 0; i < 5; i++ {
		app.collectMetrics(context.Background(), true)
		app.collectLiveness(context.Background())
	}
	<-done
	if calls.Load() != 5 {
		t.Fatal("passes did not finish")
	}
}

// Tick metrics are opt in (RSDW_TICK_METRICS): while off, the tick source never
// runs and its keys are absent, not disabled.
func TestTickSourceFollowsTheTickMetricsGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			t.Setenv("RSDW_TICK_METRICS", map[bool]string{false: "false", true: "true"}[enabled])
			app, runner, server := fixtureApp(t)
			app.collectTelemetry(context.Background())
			got := serverStatusNow(app, server)
			_, hasTick := got.Metrics["tickRate"]
			if calls := countCalls(runner, "/api/metrics"); (calls > 0) != enabled || hasTick != enabled {
				t.Fatalf("tick gate %t: /api/metrics calls=%d tickRate present=%t", enabled, calls, hasTick)
			}
			if got.Server.Status != StatusOnline {
				t.Fatalf("status = %q", got.Server.Status)
			}
			if view := defaultTelemetryConfig().view(); view.Sources["tick"].Enabled != enabled {
				t.Fatalf("telemetryConfig tick = %+v, want enabled=%t", view.Sources["tick"], enabled)
			}
		})
	}
}
