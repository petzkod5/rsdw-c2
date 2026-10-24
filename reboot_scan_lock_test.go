package main

import (
	"context"
	"testing"
	"time"
)

// Liveness, backup, and seed passes hold lifecycleMu for a state write. A
// scheduled reboot that is due while one of them holds it must still run.
func TestDueRebootRunsWhenLifecycleLockIsBrieflyHeld(t *testing.T) {
	app := newTestApp(t, false)
	runner := &rebootTestOrchestrator{}
	app.orchestrator = runner
	due := rebootAt("2026-10-21T21:00:00Z")
	now := due
	app.clock = func() time.Time { return now }
	if err := app.store.Update(func(state *State) error {
		state.Servers["scuffedtards"] = Server{ID: "scuffedtards", Name: "ScuffedTards", Status: StatusOnline}
		state.RebootSchedules["daily"] = rebootSchedule{ID: "daily", Definition: rebootDefinition{ServerID: "scuffedtards", Mode: rebootModeDaily, DailyTimes: []string{"05:00", "17:00"}, ExecutionTimezone: "America/New_York"}, Enabled: true, Revision: 1, NextRun: cloneTimePtr(&due)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for scan := 0; scan < 5; scan++ {
		app.lifecycleMu.Lock()
		go func() { time.Sleep(50 * time.Millisecond); app.lifecycleMu.Unlock() }()
		app.scanReboots(context.Background(), now)
		now = now.Add(rebootScanInterval)
	}
	time.Sleep(100 * time.Millisecond)
	app.scanReboots(context.Background(), now)
	history := app.store.Snapshot().RebootHistory
	if len(runner.calls) != 1 || len(history) == 0 || history[0].Result == rebootMissed {
		t.Fatalf("due reboot did not run: restarts=%d history=%+v", len(runner.calls), history)
	}
}
