package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The tick decoder and its fixtures predate the RSDW_TICK_METRICS gate, so the
// suite runs with the flag on; the flag-off tests below opt out explicitly.
func TestMain(m *testing.M) {
	os.Setenv("RSDW_TICK_METRICS", "true")
	os.Exit(m.Run())
}

func TestTickMetricsFlagParsing(t *testing.T) {
	for value, want := range map[string]bool{"": false, "false": false, "0": false, "garbage": false, "true": true, " TRUE ": true} {
		t.Setenv("RSDW_TICK_METRICS", value)
		if got := tickMetricsEnabled(); got != want {
			t.Fatalf("RSDW_TICK_METRICS=%q enabled = %v, want %v", value, got, want)
		}
	}
}

func TestTickMetricsDisabledSkipsGameMetricsEndpoint(t *testing.T) {
	t.Setenv("RSDW_TICK_METRICS", "false")
	k, runner, server := collectorFixture()
	got := k.collectObservation(context.Background(), server, nil)
	for _, call := range runner.calls {
		if strings.Contains(call, "/api/metrics") {
			t.Fatalf("game /api/metrics requested with tick metrics disabled: %s", call)
		}
	}
	for _, key := range tickKeys {
		if _, ok := got.metrics[key]; ok {
			t.Fatalf("tick key %s present with tick metrics disabled", key)
		}
	}
	expectMetric(t, got.metrics, "players", "available", number(2))
	expectMetric(t, got.metrics, "cpuCores", "available", number(.125))
}

func TestTickMetricsDisabledOmitsKeysFromOutputs(t *testing.T) {
	t.Setenv("RSDW_TICK_METRICS", "false")
	if len(emptyMetrics()) != len(fullMetricCatalog)-len(tickKeys) {
		t.Fatalf("unexpected metric count %d", len(emptyMetrics()))
	}
	for _, item := range activeMetricCatalog() {
		if isTickKey(item.key) {
			t.Fatalf("tick definition %s present", item.key)
		}
	}
	at := time.Now().UTC()
	metrics := emptyMetrics()
	setReading(metrics, "players", 3, at)
	server := joinObservation(Server{ID: "world"}, observation{at: at, metrics: metrics, status: StatusOnline}, at)
	for _, key := range tickKeys {
		if _, ok := server.Metrics[key]; ok {
			t.Fatalf("tick key %s in server metrics", key)
		}
	}
	viewer := viewerMetrics(server.Metrics)
	for _, key := range tickKeys {
		if _, ok := viewer[key]; ok {
			t.Fatalf("tick key %s in viewer metrics", key)
		}
	}
}

func TestTickMetricsEnabledKeepsTickCatalog(t *testing.T) {
	t.Setenv("RSDW_TICK_METRICS", "true")
	metrics := emptyMetrics()
	for _, key := range tickKeys {
		if _, ok := metrics[key]; !ok {
			t.Fatalf("tick key %s missing with tick metrics enabled", key)
		}
	}
	if len(activeMetricCatalog()) != len(fullMetricCatalog) {
		t.Fatal("tick metric definitions missing with tick metrics enabled")
	}
}

func TestTickMetricsDisabledHTTPPayloads(t *testing.T) {
	t.Setenv("RSDW_TICK_METRICS", "false")
	app, runner, _ := fixtureApp(t)
	app.auth = &Auth{token: "admin-token"}
	app.collectTelemetry(context.Background())
	httpServer := httptest.NewServer(app)
	defer httpServer.Close()
	for _, path := range []string{"/api/bootstrap", "/api/servers/world/telemetry?range=1h"} {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+path, nil)
		req.Header.Set("Authorization", "Bearer admin-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s: HTTP %d %s", path, res.StatusCode, body)
		}
		for _, key := range tickKeys {
			if strings.Contains(string(body), key) {
				t.Fatalf("%s exposes %s with tick metrics disabled: %s", path, key, body)
			}
		}
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "/api/metrics") {
			t.Fatalf("game /api/metrics requested with tick metrics disabled: %s", call)
		}
	}
}
