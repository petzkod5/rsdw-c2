package main

import (
	"context"
	"log"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	telemetryMaxAge    = 45 * time.Second
	telemetryRetention = time.Hour
	tickMaxAge         = 2 * time.Second
	tickTransportLimit = 1500 * time.Millisecond
)

var tickKeys = []string{"tickRate", "tickP50Ms", "tickP95Ms", "tickP99Ms", "tickWindowSeconds", "tickSampleCount"}

const tickSource = "game API /api/metrics engine_tick_hook UDomGameEngine::Tick"

type MetricReading struct {
	Value      *float64   `json:"value"`
	Status     string     `json:"status"`
	Source     string     `json:"source"`
	Unit       string     `json:"unit"`
	ObservedAt *time.Time `json:"observedAt"`
	Reason     string     `json:"reason"`
	// maxAge is how long after ObservedAt the reading stays fresh. The
	// collector sets it from the source's polling interval; zero means
	// telemetryMaxAge.
	maxAge time.Duration
}

// window is the freshness window of the reading.
func (r MetricReading) window() time.Duration {
	if r.maxAge > 0 {
		return r.maxAge
	}
	return telemetryMaxAge
}

// stampWindow sets the freshness window of the readings for keys.
func stampWindow(metrics map[string]MetricReading, keys []string, window time.Duration) {
	for _, key := range keys {
		reading := metrics[key]
		reading.maxAge = window
		metrics[key] = reading
	}
}

type ConnectedPlayer struct {
	Name          string `json:"name"`
	CharacterName string `json:"characterName"`
}

type RosterStatus string

const (
	RosterAvailable   RosterStatus = "available"
	RosterUnavailable RosterStatus = "unavailable"
	RosterError       RosterStatus = "error"
)

type PlayerRoster struct {
	Status     RosterStatus `json:"status,omitempty"`
	Reason     string       `json:"reason,omitempty"`
	FreshForMs int64        `json:"freshForMs,omitempty"`
	// FreshWindowMs is the whole freshness window of the player reading, so a
	// client of any role can sanity-check FreshForMs without the admin-only
	// polling configuration.
	FreshWindowMs int64             `json:"freshWindowMs,omitempty"`
	Players       []ConnectedPlayer `json:"players"`
}

func unavailableRoster(reason string) PlayerRoster {
	return PlayerRoster{Status: RosterUnavailable, Reason: reason}
}

func errorRoster(reason string) PlayerRoster {
	return PlayerRoster{Status: RosterError, Reason: reason}
}

var fullMetricCatalog = []struct{ key, unit, source, description string }{
	{"players", "players", "game API /api/players", "Connected players reported by the authenticated game API."},
	{"uptimeSeconds", "seconds", "game API /api/health", "Game API process uptime."},
	{"engineReady", "boolean", "game API /api/health", "Engine readiness reported by the game API, 0 or 1."},
	{"cpuCores", "cores", "metrics.k8s.io", "CPU cores used by the selected server container."},
	{"cpuPercent", "percent", "metrics.k8s.io / Pod limits", "CPU usage divided by the selected container CPU limit."},
	{"cpuLimitCores", "cores", "Kubernetes Pod", "CPU limit of the observed server container."},
	{"memoryUsedBytes", "bytes", "metrics.k8s.io", "Working set memory of the selected server container."},
	{"memoryLimitBytes", "bytes", "Kubernetes Pod", "Memory limit of the observed server container."},
	{"diskUsedBytes", "bytes", "Pod df data mount", "Used space on the data mount backing filesystem."},
	{"diskCapacityBytes", "bytes", "Pod df data mount", "Total capacity of the data mount backing filesystem."},
	{"diskPercent", "percent", "Pod df data mount", "Used space divided by data filesystem capacity."},
	{"inboundBytesPerSecond", "bytes/second", "Pod /proc/net/dev", "Received bytes per second across non-loopback pod interfaces."},
	{"outboundBytesPerSecond", "bytes/second", "Pod /proc/net/dev", "Transmitted bytes per second across non-loopback pod interfaces."},
	{"networkBytesPerSecond", "bytes/second", "Pod /proc/net/dev", "Combined received and transmitted pod bytes per second."},
	{"tickRate", "ticks/second", tickSource, "Completed UDomGameEngine::Tick calls divided by the observed window."},
	{"tickP50Ms", "milliseconds", tickSource, "Median elapsed execution time inside UDomGameEngine::Tick."},
	{"tickP95Ms", "milliseconds", tickSource, "95th percentile elapsed execution time inside UDomGameEngine::Tick."},
	{"tickP99Ms", "milliseconds", tickSource, "99th percentile elapsed execution time inside UDomGameEngine::Tick."},
	{"tickWindowSeconds", "seconds", tickSource, "Actual observation window for the tick snapshot."},
	{"tickSampleCount", "ticks", tickSource, "Completed calls represented by the tick snapshot."},
}

// tickMetricsEnabled reports whether RSDW_TICK_METRICS opts in to the game API tick
// snapshot, which needs the tick-patched rsdragonwilds image.
// The value is validated at startup (telemetryConfigFromEnv); anything that
// does not parse as a boolean counts as off here.
func tickMetricsEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("RSDW_TICK_METRICS")))
	return err == nil && enabled
}

func isTickKey(key string) bool {
	for _, tickKey := range tickKeys {
		if key == tickKey {
			return true
		}
	}
	return false
}

// activeMetricCatalog omits the tick metrics unless they are enabled.
func activeMetricCatalog() []struct{ key, unit, source, description string } {
	if tickMetricsEnabled() {
		return fullMetricCatalog
	}
	catalog := make([]struct{ key, unit, source, description string }, 0, len(fullMetricCatalog))
	for _, item := range fullMetricCatalog {
		if !isTickKey(item.key) {
			catalog = append(catalog, item)
		}
	}
	return catalog
}

func emptyMetrics() map[string]MetricReading {
	catalog := activeMetricCatalog()
	metrics := make(map[string]MetricReading, len(catalog))
	for _, item := range catalog {
		metrics[item.key] = MetricReading{Status: "unavailable", Source: item.source, Unit: item.unit, Reason: "No observation collected"}
	}
	return metrics
}

func setReading(metrics map[string]MetricReading, key string, value float64, at time.Time) {
	reading := metrics[key]
	reading.ObservedAt = &at
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		reading.Value, reading.Status, reading.Reason = nil, "error", "Source returned an invalid non-negative number"
	} else {
		reading.Value, reading.Status, reading.Reason = &value, "available", ""
	}
	metrics[key] = reading
}

func failReadings(metrics map[string]MetricReading, keys []string, status, reason string, at *time.Time) {
	for _, key := range keys {
		reading := metrics[key]
		reading.Value, reading.Status, reading.Reason, reading.ObservedAt = nil, status, reason, at
		metrics[key] = reading
	}
}

func freshMetrics(metrics map[string]MetricReading, now time.Time) map[string]MetricReading {
	result := emptyMetrics()
	for key, reading := range metrics {
		if reading.Status == "available" && (reading.ObservedAt == nil || now.Sub(*reading.ObservedAt) > reading.window() || reading.ObservedAt.After(now.Add(5*time.Second))) {
			reading.Value, reading.Status, reading.Reason = nil, "stale", "Source observation is outside the freshness window"
		}
		result[key] = reading
	}
	return result
}

type observation struct {
	runtime          string
	runtimeStarted   time.Time
	restartOperation string
	health           string
	at               time.Time
	metrics          map[string]MetricReading
	playerRoster     PlayerRoster
	status           Status
	image            string
	// endpoint is the join address and what is known about it. The liveness
	// pass refreshes it after the status work, never before it.
	endpoint endpointInfo
	network  *networkCounters
	// maxAge is how long the status stays valid after at. Zero means
	// telemetryMaxAge.
	maxAge time.Duration
	// healthFailures counts consecutive failed health probes on this runtime.
	healthFailures int
	// metricsAt is when the metrics tier last wrote into this observation.
	// Its readings can be newer than at, which the liveness tier stamped.
	metricsAt time.Time
}

// window is how long the observation's status stays valid.
func (o observation) window() time.Duration {
	if o.maxAge > 0 {
		return o.maxAge
	}
	return telemetryMaxAge
}

// evalAt is the time the observation's readings are judged against when it is
// replayed as history.
func (o observation) evalAt() time.Time {
	if o.metricsAt.After(o.at) {
		return o.metricsAt
	}
	return o.at
}

type livenessIssue struct {
	at     time.Time
	reason string
}

type telemetryStore struct {
	mu                sync.RWMutex
	collecting        sync.Mutex
	collectingMetrics sync.Mutex
	history           map[string][]observation
	// due is when each metric source is next collected, per server. A missing
	// entry is due immediately.
	due map[string]map[metricSource]time.Time
	// issues holds the latest inconclusive liveness pass per server, so the
	// Telemetry page can say why the status is not advancing.
	issues map[string]livenessIssue
	// kick wakes the metrics loop when a server's container changes.
	kick chan struct{}
	// slots bounds the servers collected at once across both tiers.
	// metricSlots keeps the metrics tier below that bound, so a metrics pass
	// stuck on slow sources can never take every slot from liveness.
	slots, metricSlots chan struct{}
}

const (
	collectorWorkers       = 4
	collectorMetricWorkers = collectorWorkers - 1
)

func (a *App) observations() *telemetryStore {
	a.telemetryOnce.Do(func() {
		a.telemetry = &telemetryStore{history: make(map[string][]observation), due: make(map[string]map[metricSource]time.Time), issues: make(map[string]livenessIssue), kick: make(chan struct{}, 1), slots: make(chan struct{}, collectorWorkers), metricSlots: make(chan struct{}, collectorMetricWorkers)}
	})
	return a.telemetry
}

// telemetryConfig is the effective polling configuration.
func (a *App) telemetryConfig() TelemetryConfig {
	if a.telemetryCfg.LivenessInterval == 0 {
		return defaultTelemetryConfig()
	}
	return a.telemetryCfg
}

// dueFor returns the mutable due map of a server. The caller holds mu.
func (c *telemetryStore) dueFor(id string) map[metricSource]time.Time {
	if c.due == nil {
		c.due = make(map[string]map[metricSource]time.Time)
	}
	if c.due[id] == nil {
		c.due[id] = make(map[metricSource]time.Time)
	}
	return c.due[id]
}

// dueSources lists the sources that should run now. A source is due once its
// previous start plus its interval has passed, within half a loop tick so a
// pass that ran a little late does not skip a whole tick.
func (c TelemetryConfig) dueSources(due map[metricSource]time.Time, now time.Time, force bool) map[metricSource]bool {
	slack := c.loopTick() / 2
	result := map[metricSource]bool{}
	for _, s := range metricSources {
		if !c.sourceActive(s) {
			continue
		}
		next := due[s]
		if force || next.IsZero() || !now.Before(next.Add(-slack)) {
			result[s] = true
		}
	}
	return result
}

func (c TelemetryConfig) sourceInterval(s metricSource) time.Duration {
	return c.source(s).Interval
}

// resetDue makes every source of the server due and wakes the metrics loop.
func (c *telemetryStore) resetDue(id string) {
	c.mu.Lock()
	delete(c.due, id)
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// runCollector runs the two collection tiers until ctx is cancelled. Liveness
// runs first and on its own timer; the metrics loop starts after the first
// liveness pass so it has a container to measure, and never delays status.
func (a *App) runCollector(ctx context.Context) {
	cfg := a.telemetryConfig()
	livenessDone := make(chan struct{})
	var metrics sync.WaitGroup
	metrics.Add(1)
	go func() {
		defer metrics.Done()
		select {
		case <-livenessDone:
		case <-ctx.Done():
			return
		}
		a.runMetrics(ctx, cfg)
	}()
	defer metrics.Wait()
	ticker := time.NewTicker(cfg.LivenessInterval)
	defer ticker.Stop()
	first := true
	for {
		a.collectLiveness(ctx)
		if first {
			close(livenessDone)
			first = false
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runMetrics(ctx context.Context, cfg TelemetryConfig) {
	cache := a.observations()
	ticker := time.NewTicker(cfg.loopTick())
	defer ticker.Stop()
	for {
		a.collectMetrics(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-cache.kick:
		}
	}
}

// forEachServer runs work for every server on at most four workers. Workers
// of both tiers share the four slots; tier, when set, caps one tier further.
func (a *App) forEachServer(ctx context.Context, servers map[string]Server, tier chan struct{}, work func(Server)) {
	slots := a.observations().slots
	acquire := func(ch chan struct{}) bool {
		if ch == nil {
			return true
		}
		select {
		case ch <- struct{}{}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	release := func(ch chan struct{}) {
		if ch != nil {
			<-ch
		}
	}
	jobs := make(chan Server)
	var workers sync.WaitGroup
	for i := 0; i < min(collectorWorkers, len(servers)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for server := range jobs {
				if !acquire(tier) {
					continue
				}
				if acquire(slots) {
					work(server)
					release(slots)
				}
				release(tier)
			}
		}()
	}
	for _, server := range servers {
		select {
		case jobs <- server:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

func (a *App) pruneTelemetry(snapshot State) {
	cache := a.observations()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for id := range cache.history {
		if _, exists := snapshot.Servers[id]; !exists {
			delete(cache.history, id)
		}
	}
	for id := range cache.due {
		if _, exists := snapshot.Servers[id]; !exists {
			delete(cache.due, id)
		}
	}
	for id := range cache.issues {
		if _, exists := snapshot.Servers[id]; !exists {
			delete(cache.issues, id)
		}
	}
}

// collectLiveness runs one liveness pass over every server.
func (a *App) collectLiveness(ctx context.Context) {
	cache := a.observations()
	if !cache.collecting.TryLock() {
		return
	}
	defer cache.collecting.Unlock()
	snapshot := a.store.Snapshot()
	a.pruneTelemetry(snapshot)
	a.forEachServer(ctx, snapshot.Servers, nil, func(server Server) { a.observeLiveness(ctx, server) })
}

// mergeLiveness joins a fresh liveness observation with the previous one: the
// metric readings, network baseline and roster of the same container carry
// forward with their own timestamps. A new container starts from scratch. The
// bool reports a new container, which makes the metrics tier due at once.
func mergeLiveness(previous *observation, live observation) (observation, bool) {
	if previous != nil {
		live.endpoint = previous.endpoint
	}
	newRuntime := live.runtime != "" && (previous == nil || previous.runtime != live.runtime)
	if live.runtime == "" || previous == nil || previous.runtime != live.runtime {
		return live, newRuntime
	}
	for key, reading := range previous.metrics {
		if !slices.Contains(livenessKeys, key) {
			live.metrics[key] = reading
		}
	}
	live.network, live.playerRoster, live.metricsAt = previous.network, previous.playerRoster, previous.metricsAt
	return live, false
}

// applyEndpoint stores a refreshed join address on the server's newest
// observation. It touches nothing else, so it cannot alter a status.
func (a *App) applyEndpoint(id string, endpoint endpointInfo) {
	cache := a.observations()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if history := cache.history[id]; len(history) > 0 {
		history[len(history)-1].endpoint = endpoint
	}
}

// observeLiveness records one liveness observation for the server.
func (a *App) observeLiveness(ctx context.Context, server Server) {
	cache := a.observations()
	cfg := a.telemetryConfig()
	cache.mu.RLock()
	history := cache.history[server.ID]
	historyLength := len(history)
	var historyAt time.Time
	var previous *observation
	if historyLength > 0 {
		last := history[historyLength-1]
		previous, historyAt = &last, last.at
	}
	cache.mu.RUnlock()
	result := observation{metrics: emptyMetricsFor(cfg), status: server.Status, image: server.CurrentImage, maxAge: cfg.livenessWindow()}
	merge := false
	if k, ok := a.orchestrator.(*kubeOrchestrator); ok && !a.demo {
		// The join address is auxiliary: it is read after the status is
		// recorded and acted on, so a slow Service read cannot delay or change
		// either. It stays on this worker, which keeps the number of
		// concurrent Kubernetes calls at the worker bound.
		defer func() {
			ctx, cancel := context.WithTimeout(ctx, cfg.endpointBudget())
			defer cancel()
			a.applyEndpoint(server.ID, k.refreshEndpoint(ctx, server, previous, cfg))
		}()
		var err error
		result, err = k.collectLiveness(ctx, server, previous)
		if err != nil {
			// Inconclusive: keep the last known status. It ages out on its own
			// if the evidence never comes back.
			cache.mu.Lock()
			if cache.issues == nil {
				cache.issues = make(map[string]livenessIssue)
			}
			cache.issues[server.ID] = livenessIssue{at: time.Now().UTC(), reason: err.Error()}
			cache.mu.Unlock()
			return
		}
		merge = true
	}
	result.at = time.Now().UTC()
	if a.demo {
		result.runtime = "demo/" + server.ID + "/" + server.LastRestart
		result.status = server.Status
		for key, value := range map[string]int64{"memoryUsedBytes": server.MemoryUsedBytes, "memoryLimitBytes": server.MemoryLimitBytes} {
			setReading(result.metrics, key, float64(value), result.at)
			reading := result.metrics[key]
			reading.Source = "Demo seeded server"
			result.metrics[key] = reading
		}
	}
	// The cache write below needs no lock: status must keep flowing while a
	// backup or restart holds lifecycleMu. Only alert and restart handling,
	// which mutate persisted state, wait for it.
	locked := a.lifecycleMu.TryLock()
	if locked {
		defer a.lifecycleMu.Unlock()
	}
	current := a.store.Snapshot()
	if _, ok := current.Servers[server.ID]; !ok || current.deleting(server.ID) {
		return
	}
	cache.mu.Lock()
	history = cache.history[server.ID]
	if len(history) != historyLength || historyLength > 0 && !history[len(history)-1].at.Equal(historyAt) {
		cache.mu.Unlock()
		return
	}
	newRuntime := false
	if merge {
		// Merge into the newest observation, not the one read before the pass:
		// a metrics pass may have written fresh readings in the meantime.
		var latest *observation
		if len(history) > 0 {
			last := history[len(history)-1]
			latest = &last
		}
		result, newRuntime = mergeLiveness(latest, result)
	}
	cache.history[server.ID] = retainObservations(history, result, cfg.retainedSamples())
	delete(cache.issues, server.ID)
	cache.mu.Unlock()
	if newRuntime {
		cache.resetDue(server.ID)
	}
	if !locked {
		return
	}
	var dispatch rebootDispatch
	if err := a.store.Update(func(state *State) error {
		if current, ok := state.Servers[server.ID]; ok {
			now := time.Now().UTC()
			observeAlerts(state, current, result, now)
			var err error
			dispatch, err = a.memoryPressure.observe(state, current, result, now)
			if err == nil {
				cancelObsoleteWarnings(state, now)
			}
			return err
		}
		return nil
	}); err != nil {
		log.Print("could not persist alert observations")
	} else if dispatch.server.ID != "" {
		commandErr, persistenceErr := a.dispatchRestartOperation(ctx, dispatch)
		if persistenceErr != nil {
			log.Printf("memory pressure restart result for %s: %v", server.ID, persistenceErr)
		} else if commandErr != nil {
			log.Printf("memory pressure restart command for %s: %v", server.ID, commandErr)
		}
	}
}

// collectMetrics runs one metrics pass: every source that is due, or every
// active source when force is set. Metrics never change status.
func (a *App) collectMetrics(ctx context.Context, force bool) {
	k, ok := a.orchestrator.(*kubeOrchestrator)
	if !ok || a.demo {
		return
	}
	cache := a.observations()
	if !cache.collectingMetrics.TryLock() {
		return
	}
	defer cache.collectingMetrics.Unlock()
	cfg := a.telemetryConfig()
	snapshot := a.store.Snapshot()
	a.forEachServer(ctx, snapshot.Servers, cache.metricSlots, func(server Server) {
		if !snapshot.deleting(server.ID) {
			a.observeMetrics(ctx, k, cfg, server, force)
		}
	})
}

func (a *App) observeMetrics(ctx context.Context, k *kubeOrchestrator, cfg TelemetryConfig, server Server, force bool) {
	cache := a.observations()
	now := time.Now()
	cache.mu.Lock()
	history := cache.history[server.ID]
	if len(history) == 0 || history[len(history)-1].runtime == "" {
		cache.mu.Unlock()
		return
	}
	latest := history[len(history)-1]
	state := cache.dueFor(server.ID)
	due := cfg.dueSources(state, now, force)
	for s := range due {
		state[s] = now.Add(cfg.sourceInterval(s))
	}
	cache.mu.Unlock()
	if len(due) == 0 {
		return
	}
	retry := func() {
		cache.mu.Lock()
		for s := range due {
			delete(cache.dueFor(server.ID), s)
		}
		cache.mu.Unlock()
	}
	result, ok := k.collectMetrics(ctx, server, due, latest.network)
	if !ok {
		retry()
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	history = cache.history[server.ID]
	if len(history) == 0 {
		return
	}
	last := len(history) - 1
	if result.runtime != "" && history[last].runtime != result.runtime {
		// The container changed while the pass ran; liveness already reset the
		// readings and the next pass measures the new one.
		for s := range due {
			delete(cache.dueFor(server.ID), s)
		}
		return
	}
	history[last] = patchObservation(history[last], result)
}

// patchObservation writes the readings of a metrics pass into an observation.
// It builds a new metric map: readers hold the old one.
func patchObservation(o observation, result metricsResult) observation {
	metrics := make(map[string]MetricReading, len(o.metrics))
	for key, reading := range o.metrics {
		metrics[key] = reading
	}
	for key, reading := range result.metrics {
		metrics[key] = reading
	}
	o.metrics = metrics
	if result.network != nil {
		o.network = result.network
	}
	if result.roster != nil {
		o.playerRoster = *result.roster
	}
	if result.at.After(o.metricsAt) {
		o.metricsAt = result.at
	}
	return o
}

func joinObservation(server Server, result observation, now time.Time) Server {
	lifecycleStatus := server.Status
	server.Metrics = freshMetrics(result.metrics, now)
	server.MetricsAvailable = false
	server.Players, server.UptimeSeconds, server.MemoryUsedBytes, server.MemoryLimitBytes, server.NetworkBytesPerSecond = 0, 0, 0, 0, 0
	server.CPUPercent, server.DiskPercent, server.TickRate = 0, 0, 0
	for key, reading := range server.Metrics {
		if reading.Value == nil {
			continue
		}
		server.MetricsAvailable = true
		switch key {
		case "players":
			server.Players = int(*reading.Value)
		case "uptimeSeconds":
			server.UptimeSeconds = int64(*reading.Value)
		case "cpuPercent":
			server.CPUPercent = *reading.Value
		case "memoryUsedBytes":
			server.MemoryUsedBytes = int64(*reading.Value)
		case "memoryLimitBytes":
			server.MemoryLimitBytes = int64(*reading.Value)
		case "diskPercent":
			server.DiskPercent = *reading.Value
		case "networkBytesPerSecond":
			server.NetworkBytesPerSecond = int64(*reading.Value)
		case "tickRate":
			server.TickRate = *reading.Value
		}
	}
	server.Status = StatusUnknown
	server.LastSeen = ""
	if !result.at.IsZero() && now.Sub(result.at) <= result.window() {
		server.Status = result.status
		server.LastSeen = result.at.Format(time.RFC3339Nano)
	}
	keepObservedStopped := lifecycleStatus == StatusStopped && (server.StopSource != stopSourceObserved || result.status == StatusUnknown || result.at.IsZero() || now.Sub(result.at) > result.window() || result.at.After(now))
	if lifecycleStatus == StatusDeleting || lifecycleStatus == StatusStale || keepObservedStopped {
		server.Status = lifecycleStatus
	} else if lifecycleStatus == StatusStopped && server.StopSource == stopSourceObserved {
		server.StopSource = ""
	}
	if result.image != "" {
		server.CurrentImage = result.image
	}
	if result.endpoint.state != "" {
		server.Endpoint, server.EndpointState = result.endpoint.address, result.endpoint.state
	}
	server.UpdateAvailable = server.CurrentImage != "" && server.DesiredImage != "" && server.CurrentImage != server.DesiredImage
	return server
}

func (a *App) markTelemetryPending(server Server) {
	cfg := a.telemetryConfig()
	pending := observation{at: time.Now().UTC(), metrics: emptyMetrics(), status: StatusStarting, image: server.CurrentImage, maxAge: cfg.livenessWindow()}
	cache := a.observations()
	cache.mu.Lock()
	history := cache.history[server.ID]
	if len(history) > 0 {
		pending.endpoint = history[len(history)-1].endpoint
	}
	cache.history[server.ID] = retainObservations(history, pending, cfg.retainedSamples())
	cache.mu.Unlock()
}

// retainObservations keeps the observations of the last telemetryRetention,
// and at most limit of them.
func retainObservations(history []observation, next observation, limit int) []observation {
	first := 0
	if !next.at.IsZero() {
		for first < len(history) && !history[first].at.After(next.at.Add(-telemetryRetention)) {
			first++
		}
	}
	history = history[first:]
	if len(history) >= limit {
		history = history[len(history)-limit+1:]
	}
	retained := make([]observation, len(history)+1)
	copy(retained, history)
	for i := range history {
		retained[i].playerRoster = PlayerRoster{}
	}
	retained[len(history)] = next
	return retained
}

func playerRosterFreshForMs(reading MetricReading, now time.Time) int64 {
	if reading.Status != "available" || reading.ObservedAt == nil {
		return 0
	}
	window := reading.window()
	remaining := window - now.Sub(*reading.ObservedAt)
	if remaining <= 0 {
		return 0
	}
	if remaining > window {
		remaining = window
	}
	return remaining.Milliseconds()
}

func (a *App) telemetryFor(server Server, requestedRange string) Telemetry {
	now := time.Now().UTC()
	duration := time.Minute
	if requestedRange == "5m" {
		duration = 5 * time.Minute
	}
	if requestedRange == "1h" {
		duration = time.Hour
	}
	cache := a.observations()
	cache.mu.RLock()
	history := append([]observation(nil), cache.history[server.ID]...)
	issue, hasIssue := cache.issues[server.ID]
	cache.mu.RUnlock()
	current := observation{metrics: emptyMetrics(), status: StatusUnknown}
	if a.demo {
		current.status, current.image, current.at = server.Status, server.CurrentImage, now
	}
	if len(history) > 0 {
		current = history[len(history)-1]
	}
	server = joinObservation(server, current, now)
	server.Status = restartPhase(server.Status, current, a.store.pendingRestart(server.ID))
	result := Telemetry{Server: server, Metrics: server.Metrics, MetricsAvailable: server.MetricsAvailable, Samples: []MetricSample{}, HealthChecks: []HealthCheck{}, MetricDefinitions: []MetricDefinition{}}
	result.PlayerRoster = unavailableRoster("Player count is not currently available")
	if players := result.Metrics["players"]; players.Status == "available" && players.Value != nil {
		result.PlayerRoster = current.playerRoster
		if result.PlayerRoster.Status == "" {
			if result.PlayerRoster.Players == nil {
				result.PlayerRoster = unavailableRoster("Player roster details were not provided")
			} else {
				result.PlayerRoster.Status = RosterAvailable
			}
		}
		if result.PlayerRoster.Status == RosterAvailable {
			result.PlayerRoster.FreshForMs = playerRosterFreshForMs(players, now)
			result.PlayerRoster.FreshWindowMs = players.window().Milliseconds()
		} else {
			result.PlayerRoster.FreshForMs, result.PlayerRoster.FreshWindowMs = 0, 0
		}
	} else if players.Status == "stale" {
		result.PlayerRoster = unavailableRoster("Player count is stale")
	} else if players.Status == "error" {
		result.PlayerRoster = unavailableRoster("Player count collection failed")
	}
	for _, item := range activeMetricCatalog() {
		result.MetricDefinitions = append(result.MetricDefinitions, MetricDefinition{Metric: item.key, Description: item.description})
	}
	for _, item := range history {
		if item.at.Before(now.Add(-duration)) {
			continue
		}
		sample := MetricSample{"timestamp": item.at}
		times := map[string]*time.Time{}
		statuses := map[string]string{}
		reasons := map[string]string{}
		for key, reading := range freshMetrics(item.metrics, item.evalAt()) {
			sample[key] = reading.Value
			times[key], statuses[key], reasons[key] = reading.ObservedAt, reading.Status, reading.Reason
		}
		sample["observedAt"], sample["status"], sample["reason"] = times, statuses, reasons
		result.Samples = append(result.Samples, sample)
	}
	if ready := server.Metrics["engineReady"]; ready.ObservedAt != nil {
		status := ready.Status
		if ready.Value != nil {
			status = "starting"
			if *ready.Value == 1 {
				status = "healthy"
			}
		}
		result.HealthChecks = append(result.HealthChecks, HealthCheck{Name: "Game API engine readiness", Status: status, At: *ready.ObservedAt})
	}
	if hasIssue {
		result.HealthChecks = append(result.HealthChecks, HealthCheck{Name: "Kubernetes status check", Status: "error", At: issue.at, Reason: issue.reason + "; showing the last known status"})
	}
	return result
}

// restartPhase keeps the old runtime from reading as online while a restart is
// pending. The Recreate rollout leaves it serving until Kubernetes stops it.
func restartPhase(status Status, o observation, op *RestartOperation) Status {
	switch {
	case op == nil, status == StatusStopped, status == StatusDeleting, status == StatusStale:
		return status
	case status == StatusOnline && o.restartOperation == op.ID && o.runtime != op.Runtime:
		return StatusOnline
	case status == StatusOnline:
		return StatusRestarting
	}
	return StatusOffline
}
