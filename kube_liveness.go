package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// livenessFailureThreshold is the number of consecutive failed health probes
// that moves a server out of ONLINE. One failed probe is a hiccup, not an
// outage.
const livenessFailureThreshold = 2

var errPodChanged = errors.New("Pod or server container changed during collection")

func podConditionReady(pod telemetryPod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func serverContainerReady(pod telemetryPod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "server" {
			return status.Ready
		}
	}
	return false
}

// verifyPod re-reads the Pod after a command ran in it. kubectl exec pins a
// Pod name, not a UID, so a result is only attributed to the target if the
// same container is still running.
func (k *kubeOrchestrator) verifyPod(ctx context.Context, target podTarget) (telemetryPod, error) {
	var after telemetryPod
	if err := k.kubeJSON(ctx, &after, "-n", target.pod.Metadata.Namespace, "get", "pod", target.pod.Metadata.Name, "-o", "json"); err != nil {
		return after, fmt.Errorf("Pod identity verification failed: %w", err)
	}
	if !sameContainer(target, after) {
		return after, errPodChanged
	}
	return after, nil
}

// setPodLimits records the container limits declared in the Pod spec.
func setPodLimits(metrics map[string]MetricReading, target podTarget, at time.Time) {
	for _, item := range []struct{ resource, key string }{{"cpu", "cpuLimitCores"}, {"memory", "memoryLimitBytes"}} {
		raw := target.container.Resources.Limits[item.resource]
		if raw == "" {
			failReadings(metrics, []string{item.key}, "unavailable", "Container has no explicit "+item.resource+" limit", &at)
			continue
		}
		value, err := quantityNumber(raw)
		if err != nil {
			failReadings(metrics, []string{item.key}, "error", "Invalid container limit", &at)
			continue
		}
		if value == 0 {
			failReadings(metrics, []string{item.key}, "unavailable", "Container has no effective "+item.resource+" limit", &at)
			continue
		}
		setReading(metrics, item.key, value, at)
	}
}

// markUnavailable gives every reading a reason, except readings the
// configuration turned off.
func markUnavailable(metrics map[string]MetricReading, reason string) {
	for key, reading := range metrics {
		if reading.Status != "disabled" {
			failReadings(metrics, []string{key}, "unavailable", reason, nil)
		}
	}
}

// endpointFastAttempts is how many consecutive passes may re-read the Service
// of a server whose address is not known. After that the read happens once per
// metrics interval, like a known address.
const endpointFastAttempts = 3

// endpointInfo is the join address of a server and how much is known about it.
type endpointInfo struct {
	address string
	// state is endpointResolved, endpointPending or endpointNone. Empty means
	// the Service has not been read yet.
	state string
	// at is when the Service was last read, or the read last failed.
	at time.Time
	// attempts counts the reads since the address was last resolved.
	attempts int
}

// due reports whether the Service should be read on this pass. An unknown
// address is retried on every pass for the first few attempts, since a
// LoadBalancer ingress usually appears within seconds; after that, and for a
// known address, the read happens once per metrics interval.
func (e endpointInfo) due(now time.Time, interval time.Duration) bool {
	if e.at.IsZero() {
		return true
	}
	if e.state != endpointResolved && e.attempts < endpointFastAttempts {
		return true
	}
	return now.Sub(e.at) >= interval
}

// readEndpoint reads the Service once, without the backoff.
func (k *kubeOrchestrator) readEndpoint(ctx context.Context, server Server) endpointInfo {
	return k.nextEndpoint(ctx, server, endpointInfo{}, time.Now().UTC())
}

// nextEndpoint reads the Service and folds the result into current. A failed
// read learns nothing, so the last known address and state stay.
func (k *kubeOrchestrator) nextEndpoint(ctx context.Context, server Server, current endpointInfo, now time.Time) endpointInfo {
	address, state, ok := k.lookupEndpoint(ctx, server)
	current.at = now
	switch {
	case !ok:
		if current.state == "" {
			current.state = endpointPending
		}
		if current.address == "" {
			current.attempts++
		}
	case state == endpointResolved:
		current.address, current.state, current.attempts = address, state, 0
	default:
		current.address, current.state, current.attempts = "", state, current.attempts+1
	}
	return current
}

// refreshEndpoint returns the join address for the observation that follows
// previous. The Service read is one cheap Kubernetes API call, not an exec. It
// is auxiliary: the caller runs it beside the status work and applies it after
// the status is recorded, so a slow or hung Service read cannot delay either.
func (k *kubeOrchestrator) refreshEndpoint(ctx context.Context, server Server, previous *observation, cfg TelemetryConfig) endpointInfo {
	var current endpointInfo
	if previous != nil {
		current = previous.endpoint
	}
	now := time.Now().UTC()
	if !current.due(now, cfg.MetricsInterval) {
		return current
	}
	return k.nextEndpoint(ctx, server, current, now)
}

// collectLiveness is the status tier. It reads the Deployment and Pod from the
// Kubernetes API and, for a Ready server container, makes one game API health
// probe. It never runs a metric source.
//
// The returned error means the evidence was inconclusive: the Kubernetes API
// failed or the Pod was replaced mid-pass. The caller keeps the last known
// status instead of recording a failure as a state. Definitive evidence (scaled
// to zero, no Pod, container not Ready) is returned as an observation.
func (k *kubeOrchestrator) collectLiveness(ctx context.Context, server Server, previous *observation) (observation, error) {
	cfg := k.telemetryConfig()
	ctx, cancel := context.WithTimeout(ctx, cfg.livenessBudget())
	defer cancel()
	window := cfg.livenessWindow()
	result := observation{metrics: emptyMetricsFor(cfg), playerRoster: unavailableRoster("Player roster was not collected"), status: StatusUnknown, maxAge: window}
	target, status, err := k.resolvePod(ctx, server)
	if err != nil && status == StatusUnknown {
		return observation{}, err
	}
	result.status = status
	if err != nil {
		if errors.Is(err, errScaledZero) || errors.Is(err, errNoActivePod) {
			result.health = "unhealthy"
		}
		markUnavailable(result.metrics, "Pod discovery: "+err.Error())
		return result, nil
	}
	result.image = target.container.Image
	setPodLimits(result.metrics, target, time.Now().UTC())
	stampWindow(result.metrics, livenessKeys, window)
	if target.containerID == "" || target.startedAt.IsZero() {
		result.health = "unhealthy"
		for key, reading := range result.metrics {
			if reading.Status == "unavailable" && key != "cpuLimitCores" && key != "memoryLimitBytes" {
				failReadings(result.metrics, []string{key}, "unavailable", "Server container is not running", nil)
			}
		}
		return result, nil
	}
	result.runtime, result.runtimeStarted = target.pod.Metadata.UID+"/"+target.containerID, target.startedAt
	result.restartOperation = target.pod.Metadata.Annotations[restartAnnotation]
	for _, s := range metricSources {
		if cfg.sourceActive(s) {
			failPresent(result.metrics, s.keys(), "warming_up", "Waiting for the first metrics sample")
		}
	}
	notReady := func() (observation, error) {
		result.health, result.status = "unhealthy", StatusStarting
		return result, nil
	}
	// Kubernetes already says the server is not ready: that is definitive, so
	// the game API is not probed.
	if !target.ready || !podConditionReady(target.pod) {
		failReadings(result.metrics, []string{"engineReady", "uptimeSeconds"}, "unavailable", "Server container is not ready; game API not probed", nil)
		return notReady()
	}
	data, probeErr := k.podGameAPI(ctx, target, "health")
	applyHealth(result.metrics, data, probeErr, time.Now().UTC())
	stampWindow(result.metrics, []string{"engineReady", "uptimeSeconds"}, window)
	after, err := k.verifyPod(ctx, target)
	if err != nil {
		return observation{}, err
	}
	if !serverContainerReady(after) || !podConditionReady(after) {
		return notReady()
	}
	engine := result.metrics["engineReady"]
	switch {
	case engine.Value != nil && *engine.Value == 1:
		result.health, result.status = "healthy", StatusOnline
	case engine.Value != nil:
		result.health, result.status = "unhealthy", StatusStarting
	default:
		// The probe failed. Health stays unknown (no alert evidence either
		// way); status moves only after consecutive failures.
		sameRuntime := previous != nil && previous.runtime == result.runtime
		result.healthFailures = 1
		if sameRuntime {
			result.healthFailures = previous.healthFailures + 1
		}
		switch {
		case result.healthFailures >= livenessFailureThreshold:
			result.status = StatusAttention
		case sameRuntime && previous.status == StatusOnline && time.Since(previous.at) <= previous.window():
			result.status = StatusOnline
			// The status is retained, so keep the uptime it was last
			// confirmed with instead of blanking it for one pass.
			if uptime := previous.metrics["uptimeSeconds"]; uptime.Status == "available" {
				uptime.maxAge = window
				result.metrics["uptimeSeconds"] = uptime
			}
		default:
			result.status = StatusStarting
		}
	}
	return result, nil
}

// metricsResult holds what one metrics pass collected. Only the sources that
// ran have readings; everything else keeps its previous value.
type metricsResult struct {
	runtime string
	at      time.Time
	ran     map[metricSource]bool
	metrics map[string]MetricReading
	network *networkCounters
	roster  *PlayerRoster
}

// collectMetrics is the detail tier. It runs the requested sources against the
// server's current container and returns readings only; it has no way to
// change status. The bool is false when there is no running container to
// measure.
func (k *kubeOrchestrator) collectMetrics(ctx context.Context, server Server, sources map[metricSource]bool, previousNetwork *networkCounters) (metricsResult, bool) {
	cfg := k.telemetryConfig()
	ctx, cancel := context.WithTimeout(ctx, cfg.metricsBudget())
	defer cancel()
	result := metricsResult{ran: map[metricSource]bool{}, metrics: map[string]MetricReading{}}
	measure := false
	for _, s := range metricSources {
		measure = measure || sources[s]
	}
	var target podTarget
	if measure {
		var err error
		target, _, err = k.resolvePod(ctx, server)
		if err != nil || target.containerID == "" || target.startedAt.IsZero() {
			return result, false
		}
		result.runtime = target.pod.Metadata.UID + "/" + target.containerID
	}
	if !measure {
		result.at = time.Now().UTC()
		return result, true
	}
	work := emptyMetrics()
	setPodLimits(work, target, time.Now().UTC())
	deadline, _ := ctx.Deadline()
	sourceCtx, cancelSources := context.WithDeadline(ctx, deadline.Add(-cfg.KubeTimeout))
	defer cancelSources()
	execRan := false
	for _, s := range metricSources {
		if !sources[s] {
			continue
		}
		result.ran[s] = true
		switch s {
		case sourceResources:
			k.collectResources(sourceCtx, target, work, cfg.metricWindow(s))
		case sourcePlayers:
			roster := k.collectPlayers(sourceCtx, target, work)
			result.roster = &roster
		case sourceNetwork:
			result.network = k.collectNetwork(sourceCtx, target, work, previousNetwork, cfg.metricWindow(s))
		case sourceDisk:
			k.collectDisk(sourceCtx, target, work)
		case sourceTick:
			k.collectTicks(sourceCtx, target, work)
		}
		stampWindow(work, s.keys(), cfg.metricWindow(s))
		for _, key := range s.keys() {
			result.metrics[key] = work[key]
		}
		execRan = execRan || s.execBased()
	}
	result.at = time.Now().UTC()
	if execRan {
		if _, err := k.verifyPod(ctx, target); err != nil {
			markUnavailable(result.metrics, err.Error())
			result.network = nil
			if result.roster != nil {
				roster := unavailableRoster("Pod identity verification failed")
				result.roster = &roster
			}
		}
	}
	return result, true
}
