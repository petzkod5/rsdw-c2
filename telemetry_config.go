package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Telemetry is collected in two tiers. The liveness tier reads the Deployment
// and Pod from the Kubernetes API and makes at most one game API health probe;
// it alone decides a server's status. The metrics tier fills the metric map
// (Metrics API, players, ticks, network, disk) and can never change status.
// TelemetryConfig is the validated, read-only polling configuration shared by
// both tiers; it is set from the environment at startup.
type TelemetryConfig struct {
	LivenessInterval time.Duration
	MetricsInterval  time.Duration
	KubeTimeout      time.Duration
	HealthTimeout    time.Duration
	ExecTimeout      time.Duration
	// ExecMetrics is the master switch for every metric that runs a process in
	// the game container (players, tick, network and disk).
	ExecMetrics bool
	Resources   telemetrySourceConfig
	Players     telemetrySourceConfig
	Tick        telemetrySourceConfig
	Network     telemetrySourceConfig
	Disk        telemetrySourceConfig
}

type telemetrySourceConfig struct {
	Enabled  bool
	Interval time.Duration
}

type metricSource string

const (
	sourceResources metricSource = "resources"
	sourcePlayers   metricSource = "players"
	sourceTick      metricSource = "tick"
	sourceNetwork   metricSource = "network"
	sourceDisk      metricSource = "disk"
)

// metricSources is the stable order in which a metrics pass visits sources.
var metricSources = []metricSource{sourceResources, sourcePlayers, sourceNetwork, sourceDisk, sourceTick}

var resourceKeys = []string{"cpuCores", "cpuPercent", "memoryUsedBytes"}
var networkKeys = []string{"inboundBytesPerSecond", "outboundBytesPerSecond", "networkBytesPerSecond"}
var diskKeys = []string{"diskUsedBytes", "diskCapacityBytes", "diskPercent"}

// livenessKeys are filled by the liveness tier: the health probe and the Pod
// spec. Every other metric key belongs to a metric source.
var livenessKeys = []string{"engineReady", "uptimeSeconds", "cpuLimitCores", "memoryLimitBytes"}

func (s metricSource) keys() []string {
	switch s {
	case sourceResources:
		return resourceKeys
	case sourcePlayers:
		return []string{"players"}
	case sourceTick:
		return tickKeys
	case sourceNetwork:
		return networkKeys
	case sourceDisk:
		return diskKeys
	}
	return nil
}

func (s metricSource) execBased() bool { return s != sourceResources }

func defaultTelemetryConfig() TelemetryConfig {
	return TelemetryConfig{
		LivenessInterval: 15 * time.Second,
		MetricsInterval:  time.Minute,
		KubeTimeout:      5 * time.Second,
		HealthTimeout:    4 * time.Second,
		ExecTimeout:      4 * time.Second,
		ExecMetrics:      true,
		Resources:        telemetrySourceConfig{true, time.Minute},
		Players:          telemetrySourceConfig{true, time.Minute},
		Tick:             telemetrySourceConfig{true, time.Minute},
		Network:          telemetrySourceConfig{true, time.Minute},
		Disk:             telemetrySourceConfig{true, 5 * time.Minute},
	}
}

func telemetryConfigFromEnv() (TelemetryConfig, error) {
	cfg := defaultTelemetryConfig()
	var err error
	duration := func(target *time.Duration, name string, minimum, maximum time.Duration) {
		if err != nil {
			return
		}
		raw := strings.TrimSpace(envOr(name, ""))
		if raw == "" {
			return
		}
		value, parseErr := time.ParseDuration(raw)
		if parseErr != nil || value < minimum || value > maximum {
			err = fmt.Errorf("%s must be a Go duration between %s and %s", name, minimum, maximum)
			return
		}
		*target = value
	}
	toggle := func(target *bool, name string) {
		if err != nil {
			return
		}
		raw := strings.TrimSpace(envOr(name, ""))
		if raw == "" {
			return
		}
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			err = fmt.Errorf("%s must be a boolean", name)
			return
		}
		*target = value
	}
	duration(&cfg.LivenessInterval, "RSDW_TELEMETRY_LIVENESS_INTERVAL", 5*time.Second, 5*time.Minute)
	duration(&cfg.MetricsInterval, "RSDW_TELEMETRY_METRICS_INTERVAL", 10*time.Second, 15*time.Minute)
	duration(&cfg.KubeTimeout, "RSDW_TELEMETRY_KUBE_TIMEOUT", time.Second, 30*time.Second)
	duration(&cfg.HealthTimeout, "RSDW_TELEMETRY_HEALTH_TIMEOUT", time.Second, 30*time.Second)
	duration(&cfg.ExecTimeout, "RSDW_TELEMETRY_EXEC_TIMEOUT", time.Second, 30*time.Second)
	toggle(&cfg.ExecMetrics, "RSDW_TELEMETRY_EXEC_METRICS_ENABLED")
	toggle(&cfg.Resources.Enabled, "RSDW_TELEMETRY_RESOURCES_ENABLED")
	toggle(&cfg.Players.Enabled, "RSDW_TELEMETRY_PLAYERS_ENABLED")
	toggle(&cfg.Tick.Enabled, "RSDW_TELEMETRY_TICK_ENABLED")
	toggle(&cfg.Network.Enabled, "RSDW_TELEMETRY_NETWORK_ENABLED")
	toggle(&cfg.Disk.Enabled, "RSDW_TELEMETRY_DISK_ENABLED")
	if err != nil {
		return cfg, err
	}
	// Resources and players follow the metrics interval. Tick and network
	// follow it unless overridden; disk is slow by default.
	cfg.Resources.Interval, cfg.Players.Interval = cfg.MetricsInterval, cfg.MetricsInterval
	cfg.Tick.Interval, cfg.Network.Interval = cfg.MetricsInterval, cfg.MetricsInterval
	duration(&cfg.Tick.Interval, "RSDW_TELEMETRY_TICK_INTERVAL", 10*time.Second, 15*time.Minute)
	duration(&cfg.Network.Interval, "RSDW_TELEMETRY_NETWORK_INTERVAL", 10*time.Second, 15*time.Minute)
	duration(&cfg.Disk.Interval, "RSDW_TELEMETRY_DISK_INTERVAL", time.Minute, time.Hour)
	return cfg, err
}

// validateWith rejects combinations that would silently disable a feature.
func (c TelemetryConfig) validateWith(memory MemoryPressurePolicy) error {
	if memory.Enabled && !c.Resources.Enabled {
		return fmt.Errorf("RSDW_MEMORY_PRESSURE_ENABLED requires RSDW_TELEMETRY_RESOURCES_ENABLED, which supplies the memory samples")
	}
	return nil
}

func (c TelemetryConfig) source(s metricSource) telemetrySourceConfig {
	switch s {
	case sourceResources:
		return c.Resources
	case sourcePlayers:
		return c.Players
	case sourceTick:
		return c.Tick
	case sourceNetwork:
		return c.Network
	case sourceDisk:
		return c.Disk
	}
	return telemetrySourceConfig{}
}

// sourceActive reports whether a source runs: it is enabled and, for exec
// based sources, the exec master switch is on.
func (c TelemetryConfig) sourceActive(s metricSource) bool {
	if s.execBased() && !c.ExecMetrics {
		return false
	}
	return c.source(s).Enabled
}

// livenessWindow is how long a liveness observation keeps its status.
func (c TelemetryConfig) livenessWindow() time.Duration {
	return freshnessWindow(c.LivenessInterval)
}

// metricWindow is how long a reading from the source stays fresh: three
// polling intervals, never less than the global 45 second minimum.
func (c TelemetryConfig) metricWindow(s metricSource) time.Duration {
	return freshnessWindow(c.source(s).Interval)
}

func freshnessWindow(interval time.Duration) time.Duration {
	if window := 3 * interval; window > telemetryMaxAge {
		return window
	}
	return telemetryMaxAge
}

// loopTick is how often the metrics loop wakes to look for due sources: the
// shortest active interval, and never slower than the metrics interval that
// also refreshes the endpoint.
func (c TelemetryConfig) loopTick() time.Duration {
	tick := c.MetricsInterval
	for _, s := range metricSources {
		if c.sourceActive(s) && c.source(s).Interval < tick {
			tick = c.source(s).Interval
		}
	}
	return tick
}

// retainedSamples is how many liveness observations cover the retention
// period: one per liveness interval, 240 at the default 15 seconds.
func (c TelemetryConfig) retainedSamples() int {
	return max(12, int(telemetryRetention/c.LivenessInterval))
}

// livenessBudget bounds one liveness pass: three discovery reads, one health
// probe and one identity re-check.
func (c TelemetryConfig) livenessBudget() time.Duration {
	return 4*c.KubeTimeout + c.HealthTimeout
}

// metricsBudget bounds one metrics pass for the sources that are active: Pod
// discovery, the endpoint lookup, the Metrics API read, one identity re-check
// and one exec per active exec source.
func (c TelemetryConfig) metricsBudget() time.Duration {
	budget := 7 * c.KubeTimeout
	for _, s := range metricSources {
		if c.sourceActive(s) && s.execBased() {
			budget += c.ExecTimeout
		}
	}
	return budget
}

// estimatedExecsPerMinute is the number of kubectl exec calls per server and
// minute that the configuration produces while the server is ready.
func (c TelemetryConfig) estimatedExecsPerMinute() float64 {
	execs := 60 / c.LivenessInterval.Seconds()
	for _, s := range metricSources {
		if s.execBased() && c.sourceActive(s) {
			execs += 60 / c.source(s).Interval.Seconds()
		}
	}
	return execs
}

type TelemetrySourceView struct {
	Enabled         bool    `json:"enabled"`
	IntervalSeconds float64 `json:"intervalSeconds"`
	Exec            bool    `json:"exec"`
}

// TelemetryConfigView is the read-only effective configuration returned by
// GET /api/bootstrap. Source "enabled" is the effective value: an exec based
// source is reported off when execMetricsEnabled is false.
type TelemetryConfigView struct {
	LivenessIntervalSeconds  float64                        `json:"livenessIntervalSeconds"`
	MetricsIntervalSeconds   float64                        `json:"metricsIntervalSeconds"`
	KubeTimeoutSeconds       float64                        `json:"kubeTimeoutSeconds"`
	HealthTimeoutSeconds     float64                        `json:"healthTimeoutSeconds"`
	ExecTimeoutSeconds       float64                        `json:"execTimeoutSeconds"`
	ExecMetricsEnabled       bool                           `json:"execMetricsEnabled"`
	Sources                  map[string]TelemetrySourceView `json:"sources"`
	EstimatedExecsPerMinute  float64                        `json:"estimatedExecsPerMinutePerServer"`
	LivenessFreshnessSeconds float64                        `json:"livenessFreshnessSeconds"`
	MetricFreshnessSeconds   map[string]float64             `json:"metricFreshnessSeconds"`
}

func (c TelemetryConfig) view() TelemetryConfigView {
	view := TelemetryConfigView{
		LivenessIntervalSeconds:  c.LivenessInterval.Seconds(),
		MetricsIntervalSeconds:   c.MetricsInterval.Seconds(),
		KubeTimeoutSeconds:       c.KubeTimeout.Seconds(),
		HealthTimeoutSeconds:     c.HealthTimeout.Seconds(),
		ExecTimeoutSeconds:       c.ExecTimeout.Seconds(),
		ExecMetricsEnabled:       c.ExecMetrics,
		Sources:                  map[string]TelemetrySourceView{},
		EstimatedExecsPerMinute:  c.estimatedExecsPerMinute(),
		LivenessFreshnessSeconds: c.livenessWindow().Seconds(),
		MetricFreshnessSeconds:   map[string]float64{},
	}
	for _, s := range metricSources {
		view.Sources[string(s)] = TelemetrySourceView{Enabled: c.sourceActive(s), IntervalSeconds: c.source(s).Interval.Seconds(), Exec: s.execBased()}
		view.MetricFreshnessSeconds[string(s)] = c.metricWindow(s).Seconds()
	}
	return view
}

// emptyMetricsFor is emptyMetrics with every source the configuration turned
// off marked disabled, so it is never mistaken for a failure.
func emptyMetricsFor(c TelemetryConfig) map[string]MetricReading {
	metrics := emptyMetrics()
	for _, s := range metricSources {
		if !c.sourceActive(s) {
			failPresent(metrics, s.keys(), "disabled", disabledReason(c, s))
		}
	}
	return metrics
}

// failPresent is failReadings for the keys the metric map already has: a
// catalog without some metric (for example ticks) never gains it here.
func failPresent(metrics map[string]MetricReading, keys []string, status, reason string) {
	for _, key := range keys {
		if _, ok := metrics[key]; ok {
			failReadings(metrics, []string{key}, status, reason, nil)
		}
	}
}

func disabledReason(c TelemetryConfig, s metricSource) string {
	if s.execBased() && !c.ExecMetrics {
		return "Turned off by configuration (exec metrics disabled)"
	}
	return "Turned off by configuration"
}
