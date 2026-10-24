package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func rolledOutPod(opID string, started time.Time) string {
	pod := strings.ReplaceAll(fixturePod(), "container-id", "replacement-container")
	pod = strings.ReplaceAll(pod, `"uid":"pod-uid"`, `"uid":"pod-uid-2","annotations":{"`+restartAnnotation+`":"`+opID+`"}`)
	return strings.ReplaceAll(pod, "2026-01-01T00:00:00Z", started.UTC().Format(time.RFC3339))
}

func rollOut(runner *telemetryRunner, opID string, started time.Time) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.override = func(call string) ([]byte, error, bool) {
		pod := rolledOutPod(opID, started)
		switch {
		case strings.Contains(call, "get pods"):
			return []byte(`{"items":[` + pod + `]}`), nil, true
		case strings.Contains(call, "get pod world-pod"):
			return []byte(pod), nil, true
		case strings.Contains(call, " patch "):
			return []byte("{}"), nil, true
		}
		return nil, nil, false
	}
}

func assertRestartReconciles(t *testing.T, app *App, runner *telemetryRunner) {
	t.Helper()
	op := app.store.Snapshot().Producers["world"].Restart
	if op == nil {
		t.Fatal("no restart operation recorded")
	}
	if countCalls(runner, "patch deployment/world-rsdragonwilds") != 1 {
		t.Fatalf("deployment was not patched: %v", runner.calls)
	}
	rollOut(runner, op.ID, time.Now().Add(time.Second))
	time.Sleep(1100 * time.Millisecond)
	app.collectLiveness(context.Background())
	state := app.store.Snapshot()
	if state.Producers["world"].Restart != nil {
		t.Fatalf("restart %s never reconciled after the replacement pod became ready", op.ID)
	}
	completed := false
	for _, event := range state.Events {
		completed = completed || (event.Kind == RestartCompleted && event.OperationID == op.ID)
	}
	if !completed {
		t.Fatal("no restart_completed event for Discord")
	}
}

func TestReproManualRestartReconciles(t *testing.T) {
	app, runner, _ := fixtureApp(t)
	runner.override = func(call string) ([]byte, error, bool) {
		return []byte("{}"), nil, strings.Contains(call, " patch ")
	}
	app.collectTelemetry(context.Background())
	dispatch, err := app.claimManualRestart("world", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if cmdErr, perr := app.dispatchRestartOperation(context.Background(), dispatch); cmdErr != nil || perr != nil {
		t.Fatal(cmdErr, perr)
	}
	assertRestartReconciles(t, app, runner)
}

func TestReproScheduledRestartReconciles(t *testing.T) {
	app, runner, _ := fixtureApp(t)
	runner.override = func(call string) ([]byte, error, bool) {
		return []byte("{}"), nil, strings.Contains(call, " patch ")
	}
	app.collectTelemetry(context.Background())
	now := time.Now().UTC()
	due := now.Add(-5 * time.Second)
	anchor := now.Add(-12 * time.Hour)
	if err := app.store.Update(func(state *State) error {
		state.initReboots()
		state.RebootSchedules["twice"] = rebootSchedule{ID: "twice", Definition: rebootDefinition{ServerID: "world", Mode: rebootModeInterval, IntervalValue: 12, IntervalUnit: "hours", ExecutionTimezone: "UTC"}, Enabled: true, Revision: 1, CreatedAt: anchor, UpdatedAt: anchor, IntervalAnchor: &anchor, NextRun: &due}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.scanReboots(context.Background(), now)
	assertRestartReconciles(t, app, runner)
}

func TestRestartStatusShowsRestartingThenOfflineThenOnline(t *testing.T) {
	app, runner, server := fixtureApp(t)
	runner.override = func(call string) ([]byte, error, bool) {
		return []byte("{}"), nil, strings.Contains(call, " patch ")
	}
	app.collectTelemetry(context.Background())
	shown := func() Status { return app.telemetryFor(app.store.Snapshot().Servers[server.ID], "60s").Server.Status }
	dispatch, err := app.claimManualRestart(server.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := app.dispatchRestartOperation(context.Background(), dispatch); perr != nil {
		t.Fatal(perr)
	}
	app.collectLiveness(context.Background())
	if got := shown(); got != StatusRestarting {
		t.Fatalf("old runtime still serving during restart shows %q, want %q", got, StatusRestarting)
	}
	runner.mu.Lock()
	runner.override = func(call string) ([]byte, error, bool) {
		if strings.Contains(call, "get pods") {
			return []byte(`{"items":[]}`), nil, true
		}
		return nil, nil, false
	}
	runner.mu.Unlock()
	app.collectLiveness(context.Background())
	if got := shown(); got != StatusOffline {
		t.Fatalf("no pod during restart shows %q, want %q", got, StatusOffline)
	}
	rollOut(runner, dispatch.op.ID, time.Now().Add(time.Second))
	time.Sleep(1100 * time.Millisecond)
	app.collectLiveness(context.Background())
	if got := shown(); got != StatusOnline {
		t.Fatalf("ready replacement shows %q, want %q", got, StatusOnline)
	}
	if last := app.store.Snapshot().Servers[server.ID].LastRestart; last == "" {
		t.Fatal("last restart timestamp not recorded")
	}
}
