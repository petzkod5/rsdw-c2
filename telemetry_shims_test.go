package main

import (
	"context"
	"time"
)

// These helpers adapt the two-tier collector to tests written around a single
// collection pass. telemetry_regression_test.go uses only helpers that also
// exist on the pre-tier collector, so it can be run against older code.

// collectObservation runs both collection tiers once, with every metric source
// due, and joins them the way the collector does. Tests that assert on source
// behaviour use it to see one complete observation.
func (k *kubeOrchestrator) collectObservation(ctx context.Context, server Server, previousNetwork *networkCounters) observation {
	cfg := k.telemetryConfig()
	live, err := k.collectLiveness(ctx, server, nil)
	if err != nil {
		result := observation{metrics: emptyMetrics(), playerRoster: unavailableRoster("Player roster was not collected"), status: StatusUnknown}
		markUnavailable(result.metrics, err.Error())
		return result
	}
	if live.runtime == "" {
		return live
	}
	sources := map[metricSource]bool{sourceEndpoint: true}
	for _, s := range metricSources {
		if cfg.sourceActive(s) {
			sources[s] = true
		}
	}
	if res, ok := k.collectMetrics(ctx, server, sources, previousNetwork); ok {
		live = patchObservation(live, res)
	}
	return live
}

// collectTelemetry runs one liveness pass and one metrics pass with every
// source due.
func (a *App) collectTelemetry(ctx context.Context) {
	a.collectLiveness(ctx)
	a.collectMetrics(ctx, true)
}

// shortenMetricTimeouts makes metric sources give up quickly so a test of
// hanging sources does not wait out the production timeouts.
func shortenMetricTimeouts(k *kubeOrchestrator) {
	cfg := k.telemetryConfig()
	cfg.ExecTimeout = 300 * time.Millisecond
	k.telemetry = cfg
}
