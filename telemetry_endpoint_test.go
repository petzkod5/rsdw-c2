package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	clusterIPService   = `{"spec":{"type":"ClusterIP","ports":[{"name":"game","port":7777}]},"status":{"loadBalancer":{}}}`
	pendingBalancer    = `{"spec":{"type":"LoadBalancer","ports":[{"name":"game","port":7777,"nodePort":30100}]},"status":{"loadBalancer":{}}}`
	readyBalancer      = `{"spec":{"type":"LoadBalancer","ports":[{"name":"game","port":7777,"nodePort":30100}]},"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.42"}]}}}`
	nodePortService    = `{"spec":{"type":"NodePort","ports":[{"name":"game","port":7777,"nodePort":30100}]},"status":{"loadBalancer":{}}}`
	nodeWithInternalIP = `{"items":[{"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.7"}]}}]}`
	nodeWithoutAddress = `{"items":[{"status":{"addresses":[]}}]}`
	// pastMetricsInterval is older than the default metrics interval.
	pastMetricsInterval = 2 * time.Minute
)

// serviceFixture serves the chart's Service and the node list from variables
// the test changes between passes, and counts the Service reads.
type serviceFixture struct {
	service, nodes string
	reads          int
	failing        bool
}

func (f *serviceFixture) override(call string) ([]byte, error, bool) {
	switch {
	case strings.Contains(call, "get service"):
		f.reads++
		if f.failing {
			return nil, context.DeadlineExceeded, true
		}
		return []byte(f.service), nil, true
	case strings.Contains(call, "get nodes"):
		return []byte(f.nodes), nil, true
	}
	return nil, nil, false
}

func endpointNow(app *App, server Server) Server {
	return serverStatusNow(app, server).Server
}

// ageEndpoint makes the last Service read look old, so the metrics-interval
// schedule is due without waiting for it.
func ageEndpoint(app *App, server Server, age time.Duration) {
	cache := app.observations()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	history := cache.history[server.ID]
	history[len(history)-1].endpoint.at = time.Now().Add(-age)
}

func TestEndpointStatesAreClassified(t *testing.T) {
	for _, test := range []struct {
		name, service, nodes string
		address, state       string
	}{
		{"ClusterIP has no external endpoint", clusterIPService, nodeWithInternalIP, "", "none"},
		{"LoadBalancer ingress hostname", `{"spec":{"type":"LoadBalancer","ports":[{"name":"game","port":7777}]},"status":{"loadBalancer":{"ingress":[{"hostname":"play.example.com"}]}}}`, nodeWithoutAddress, "play.example.com:7777", "resolved"},
		{"LoadBalancer without ingress is pending", pendingBalancer, nodeWithoutAddress, "", "pending"},
		{"LoadBalancer without ingress falls back to a node", pendingBalancer, nodeWithInternalIP, "10.0.0.7:30100", "resolved"},
		{"NodePort without a node address is pending", nodePortService, nodeWithoutAddress, "", "pending"},
		{"NodePort with a node address", nodePortService, nodeWithInternalIP, "10.0.0.7:30100", "resolved"},
		{"Service without a game port", `{"spec":{"type":"ClusterIP","ports":[{"name":"query","port":27015}]}}`, nodeWithInternalIP, "", "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, runner, server := fixtureApp(t)
			fixture := &serviceFixture{service: test.service, nodes: test.nodes}
			runner.override = fixture.override
			app.collectLiveness(context.Background())
			got := endpointNow(app, server)
			if got.Endpoint != test.address || got.EndpointState != test.state || got.Status != StatusOnline {
				t.Fatalf("endpoint %q state %q status %q, want %q %q online", got.Endpoint, got.EndpointState, got.Status, test.address, test.state)
			}
		})
	}
}

func TestEndpointStatePayload(t *testing.T) {
	data, err := Server{ID: "world", Endpoint: "203.0.113.42:7777", EndpointState: "resolved"}.MarshalJSON()
	if err != nil || !strings.Contains(string(data), `"endpoint":"203.0.113.42:7777"`) || !strings.Contains(string(data), `"endpointState":"resolved"`) {
		t.Fatalf("payload %s (%v)", data, err)
	}
	data, _ = Server{ID: "world"}.MarshalJSON()
	if !strings.Contains(string(data), `"endpoint":""`) || strings.Contains(string(data), "endpointState") {
		t.Fatalf("a server that has not been read must keep the old shape: %s", data)
	}
}

func TestLoadBalancerPendingThenResolved(t *testing.T) {
	app, runner, server := fixtureApp(t)
	fixture := &serviceFixture{service: pendingBalancer, nodes: nodeWithoutAddress}
	runner.override = fixture.override
	app.collectLiveness(context.Background())
	if got := endpointNow(app, server); got.Endpoint != "" || got.EndpointState != "pending" {
		t.Fatalf("before the ingress: %q %q", got.Endpoint, got.EndpointState)
	}
	fixture.service = readyBalancer
	app.collectLiveness(context.Background())
	if got := endpointNow(app, server); got.Endpoint != "203.0.113.42:7777" || got.EndpointState != "resolved" {
		t.Fatalf("after the ingress: %q %q", got.Endpoint, got.EndpointState)
	}
	// Once resolved, a later pass does not read the Service until the metrics
	// interval has passed.
	reads := fixture.reads
	app.collectLiveness(context.Background())
	if fixture.reads != reads {
		t.Fatalf("a resolved address was re-read: %d extra reads", fixture.reads-reads)
	}
	// A Service that turns into ClusterIP drops the address instead of keeping
	// a stale one.
	fixture.service = clusterIPService
	ageEndpoint(app, server, pastMetricsInterval)
	app.collectLiveness(context.Background())
	if got := endpointNow(app, server); got.Endpoint != "" || got.EndpointState != "none" {
		t.Fatalf("after the Service lost its address: %q %q", got.Endpoint, got.EndpointState)
	}
}

// An unresolved address is read on each of the first three passes, then once
// per metrics interval, whatever the reason it is unresolved.
func TestEndpointReadsBackOffToTheMetricsInterval(t *testing.T) {
	for _, test := range []struct{ name, service, nodes, state string }{
		{"ClusterIP", clusterIPService, nodeWithInternalIP, "none"},
		{"pending LoadBalancer", pendingBalancer, nodeWithoutAddress, "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, runner, server := fixtureApp(t)
			fixture := &serviceFixture{service: test.service, nodes: test.nodes}
			runner.override = fixture.override
			var schedule []int
			for pass := 1; pass <= 8; pass++ {
				app.collectLiveness(context.Background())
				schedule = append(schedule, fixture.reads)
				if got := endpointNow(app, server); got.EndpointState != test.state || got.Status != StatusOnline {
					t.Fatalf("pass %d: state %q status %q", pass, got.EndpointState, got.Status)
				}
			}
			if want := []int{1, 2, 3, 3, 3, 3, 3, 3}; !slices.Equal(schedule, want) {
				t.Fatalf("Service reads after each pass = %v, want %v", schedule, want)
			}
			// One metrics interval later the next read happens, and the
			// schedule goes back to waiting.
			ageEndpoint(app, server, pastMetricsInterval)
			app.collectLiveness(context.Background())
			app.collectLiveness(context.Background())
			if fixture.reads != 4 {
				t.Fatalf("reads = %d, want one more after the metrics interval", fixture.reads)
			}
			// An address that appears is picked up by that next read.
			fixture.service = readyBalancer
			ageEndpoint(app, server, pastMetricsInterval)
			app.collectLiveness(context.Background())
			if got := endpointNow(app, server); got.Endpoint != "203.0.113.42:7777" || got.EndpointState != "resolved" {
				t.Fatalf("late address: %q %q", got.Endpoint, got.EndpointState)
			}
		})
	}
}

// A failing Service read learns nothing: the address and state stay.
func TestFailedEndpointReadKeepsWhatWasKnown(t *testing.T) {
	app, runner, server := fixtureApp(t)
	fixture := &serviceFixture{service: readyBalancer}
	runner.override = fixture.override
	app.collectLiveness(context.Background())
	fixture.failing = true
	ageEndpoint(app, server, pastMetricsInterval)
	app.collectLiveness(context.Background())
	if got := endpointNow(app, server); got.Endpoint != "203.0.113.42:7777" || got.EndpointState != "resolved" || fixture.reads != 2 {
		t.Fatalf("after a failed refresh: %q %q after %d reads", got.Endpoint, got.EndpointState, fixture.reads)
	}
}

// A hung Service read cannot delay the status. The health probe starts at the
// beginning of the pass, the status is recorded before the Service read even
// starts, and the pass issues one health exec.
func TestHungServiceReadDoesNotDelayStatus(t *testing.T) {
	app, runner, server := fixtureApp(t)
	k := app.orchestrator.(*kubeOrchestrator)
	k.telemetry = defaultTelemetryConfig()
	k.telemetry.KubeTimeout, k.telemetry.HealthTimeout = 400*time.Millisecond, 400*time.Millisecond
	cfg := k.telemetry
	start := time.Now()
	var probeAt time.Duration
	probed := false
	var statusWhenServiceHung Status
	statusReads := 0
	k.runner = &scriptedRunner{
		base: runner,
		delay: func(call string) time.Duration {
			switch {
			case strings.Contains(call, "/api/health"):
				probeAt, probed = time.Since(start), true
			case strings.Contains(call, "get service"):
				statusWhenServiceHung = serverStatusNow(app, server).Server.Status
				return time.Hour
			case strings.Contains(call, "get "):
				statusReads++
			}
			return 0
		},
	}
	app.collectLiveness(context.Background())
	elapsed := time.Since(start)
	if statusWhenServiceHung != StatusOnline {
		t.Fatalf("status when the Service read started = %q, want ONLINE already recorded", statusWhenServiceHung)
	}
	if !probed || probeAt >= cfg.HealthTimeout {
		t.Fatalf("health probe started %v into the pass, want well within the %v health timeout", probeAt, cfg.HealthTimeout)
	}
	if got := endpointNow(app, server); got.Status != StatusOnline || got.EndpointState != "pending" || got.Endpoint != "" {
		t.Fatalf("status %q endpoint %q state %q", got.Status, got.Endpoint, got.EndpointState)
	}
	// Accounting: the status work is three discovery reads, one identity
	// re-check and one health exec, all inside livenessBudget. The Service
	// read has its own bound on top and is cut off by it.
	healthExecs := 0
	for _, call := range runner.calls {
		if strings.Contains(call, "/api/health") {
			healthExecs++
		}
	}
	if healthExecs != 1 || statusReads != 4 {
		t.Fatalf("health execs %d, status reads %d, want 1 and 4", healthExecs, statusReads)
	}
	if cfg.livenessBudget() != time.Duration(statusReads)*cfg.KubeTimeout+cfg.HealthTimeout {
		t.Fatalf("livenessBudget %v does not match %d reads and a health probe", cfg.livenessBudget(), statusReads)
	}
	if elapsed < cfg.KubeTimeout || elapsed > cfg.livenessBudget()+cfg.endpointBudget() {
		t.Fatalf("pass took %v, want the Service read cut off by its own bound (%v at most)", elapsed, cfg.endpointBudget())
	}
}

// Every liveness pass makes at most one health exec, whatever the endpoint
// does.
func TestLivenessPassMakesOneHealthExec(t *testing.T) {
	app, runner, _ := fixtureApp(t)
	fixture := &serviceFixture{service: pendingBalancer, nodes: nodeWithoutAddress}
	runner.override = fixture.override
	for pass := 1; pass <= 5; pass++ {
		before := len(runner.calls)
		app.collectLiveness(context.Background())
		execs := 0
		for _, call := range runner.calls[before:] {
			if strings.Contains(call, "exec ") {
				execs++
			}
		}
		if execs != 1 {
			t.Fatalf("pass %d made %d execs, want 1", pass, execs)
		}
	}
}
