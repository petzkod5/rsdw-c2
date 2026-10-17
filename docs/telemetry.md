# Telemetry sources and limits

C2 collects telemetry in the background, even when no browser is open, in two tiers that run on their own timers. It keeps up to one hour of observations in memory. A dashboard service restart clears history. Collection failures create gaps in the metric map and charts, not invented replacement values.

## Collection tiers

| Tier | Decides | Reads | Default interval |
| --- | --- | --- | --- |
| Liveness | The server status: online, starting, needs attention, stopped, unknown | The Deployment, ReplicaSets, and Pod from the Kubernetes API, plus at most one game `/api/health` probe (one `kubectl exec`) | 15 seconds |
| Metrics | Nothing but the numbers on the Telemetry page | Kubernetes Metrics API (CPU, memory), game `/api/players`, `/proc/net/dev`, game `/api/metrics` (tick), and `df` (disk), each on its own timer | 60 seconds; disk 5 minutes |

Each tier has its own loop. Every pass visits the servers on at most four workers, and a pass of one tier never waits for the other. A pass that is still running when the next one is due makes that tick get skipped, so a slow source or a large fleet lengthens the interval. The two tiers share the four workers; the metrics tier is held to three of them so a metrics pass stuck on slow sources can never keep liveness from running. Metric sources that run a process in the game container (players, tick, network, disk) are called exec metrics. Only the health probe is an exec in the liveness tier.

The metrics tier measures the container the liveness tier last observed. When that container is replaced, readings from the old one are dropped, the network baseline restarts, and the metrics tier runs again at once.

### Status

Status comes from the liveness tier alone. A metric that fails, times out, is stale, or is turned off never changes it and never clears it.

| Evidence | Status |
| --- | --- |
| Deployment has zero replicas | `stopped`, at once |
| No Pod, or the `server` container is not running | `starting`, at once |
| The container or Pod is not Ready | `starting`, at once. The game API is not probed. |
| Ready, and `/api/health` reports the engine ready | `online` |
| Ready, and `/api/health` reports the engine not ready | `starting` |
| Ready, and one `/api/health` probe failed | Stays `online` if it was online on the same container. Otherwise `starting`. |
| Ready, and two consecutive `/api/health` probes failed | `attention` |
| The Kubernetes API failed, returned unusable data, or the Pod was replaced during the pass | No change. The last known status stays, with its original `lastSeen`. The Telemetry page lists a failed "Kubernetes status check" with the reason. |
| No successful liveness pass inside the liveness freshness window | `unknown` |

The dashboard labels these Online, Starting, Needs attention, Stopped, and Unknown. Dashboard rows show "Collecting" or a dash for a metric that has no reading, never a failure message. The per-metric reasons are on the Telemetry page, in the metric sources table.

Server-down and server-recovered alerts keep their thresholds: three unhealthy observations spanning thirty seconds after a healthy baseline, then two healthy observations spanning fifteen seconds. A failed health probe records no health either way, so it neither starts nor extends an outage. Player alerts and memory-pressure restarts read the carried-forward readings of the metrics tier and treat a missing, stale, or disabled reading as no new information.

### Freshness

Freshness is per tier. The liveness window is three liveness intervals, never less than 45 seconds: 45 seconds by default. A metric reading stays fresh for three intervals of its own source, never less than 45 seconds: 3 minutes for resources, players, tick, and network and 15 minutes for disk by default. The window starts at the source's observation time, so a value is never shown as newer than the instrument that produced it. An older reading is `stale`.

Between metrics passes, each liveness observation carries the latest readings forward with their original timestamps. Charts place a point at its observation time, skip a repeated one, and break the line when two readings are further apart than a few intervals of that metric.

## Configuration

Set by environment variable, or by the `telemetry` values of the Helm chart. Intervals and timeouts are Go durations. A value outside its bounds, or an unparsable one, stops C2 at startup with an error naming the variable. `GET /api/bootstrap` returns the effective configuration to administrators as `telemetryConfig`, including the estimated exec calls per minute and server; it is read-only.

| Environment variable | Helm value | Default | Bounds | Meaning |
| --- | --- | --- | --- | --- |
| `RSDW_TELEMETRY_LIVENESS_INTERVAL` | `telemetry.livenessInterval` | `15s` | 5s to 5m | Liveness loop |
| `RSDW_TELEMETRY_METRICS_INTERVAL` | `telemetry.metricsInterval` | `60s` | 10s to 15m | Base interval of the metrics loop; resources and players, and the default for tick and network |
| `RSDW_TELEMETRY_EXEC_METRICS_ENABLED` | `telemetry.execMetrics.enabled` | `true` | boolean | Master switch for every exec metric: players, tick, network, disk |
| `RSDW_TELEMETRY_RESOURCES_ENABLED` | `telemetry.resources.enabled` | `true` | boolean | CPU and memory from the Metrics API. Required when `memoryPressure.enabled` is true. |
| `RSDW_TELEMETRY_PLAYERS_ENABLED` | `telemetry.players.enabled` | `true` | boolean | Game `/api/players` |
| `RSDW_TELEMETRY_TICK_ENABLED` | `telemetry.tick.enabled` | `true` | boolean | Game `/api/metrics` |
| `RSDW_TELEMETRY_TICK_INTERVAL` | `telemetry.tick.interval` | metrics interval | 10s to 15m | Tick poll |
| `RSDW_TELEMETRY_NETWORK_ENABLED` | `telemetry.network.enabled` | `true` | boolean | `/proc/net/dev` |
| `RSDW_TELEMETRY_NETWORK_INTERVAL` | `telemetry.network.interval` | metrics interval | 10s to 15m | Network poll. Rates need two samples. |
| `RSDW_TELEMETRY_DISK_ENABLED` | `telemetry.disk.enabled` | `true` | boolean | `df` on the data mount |
| `RSDW_TELEMETRY_DISK_INTERVAL` | `telemetry.disk.interval` | `5m` | 1m to 1h | Disk poll. Disk changes slowly. |
| `RSDW_TELEMETRY_KUBE_TIMEOUT` | `telemetry.timeouts.kube` | `5s` | 1s to 30s | Each `kubectl get` |
| `RSDW_TELEMETRY_HEALTH_TIMEOUT` | `telemetry.timeouts.health` | `4s` | 1s to 30s | The `/api/health` probe |
| `RSDW_TELEMETRY_EXEC_TIMEOUT` | `telemetry.timeouts.exec` | `4s` | 1s to 30s | Each exec metric |

A source that is turned off reports status `disabled` with the reason "Turned off by configuration". It does not count towards `metricsAvailable` and raises no alert. The tick snapshot additionally needs the tick-enabled game image.

### Load on the game container

Every exec starts short-lived processes (a shell, `cat`, `curl`, or `df`) inside the game container, charged to its CPU limit. With the defaults, each ready server receives about 7.2 execs per minute: four health probes, one each for players, network, and tick, and one disk read every five minutes. The previous single 15-second pass made 20.

**Recommendation.** Keep the defaults on a node with CPU to spare. If the game stutters or the CPU limit is tight, set `telemetry.execMetrics.enabled=false`. The only exec left is the health probe (four per minute). Status stays reliable, CPU and memory keep coming from the Metrics API, and players, network, disk, and tick rate show as disabled. Raise `telemetry.livenessInterval` for fewer probes at the cost of slower outage detection: with two failed probes needed, an outage is noticed after about two intervals.

![Telemetry page with range controls, metric cards, charts, and resource usage.](screenshots/telemetry.png)

| Measurement | Source | Meaning |
| --- | --- | --- |
| Players | Authenticated game `/api/players` | API-reported connected count. The current upstream API can return an empty roster after an internal lookup failure. |
| Engine readiness | Game `/api/health` | Whether the mod reports that the engine is ready. |
| API uptime | Game `/api/health` | Mod process uptime, not a world availability percentage. |
| CPU cores | Kubernetes Metrics API | Average CPU use of the `server` container over the source's measurement window. |
| CPU limit used | CPU cores and the running Pod's CPU limit | Percentage of the container's configured ceiling. Without a limit, cores remain available but the percentage does not. |
| Memory working set | Kubernetes Metrics API | The `server` container's working-set bytes. Sidecar memory is excluded. |
| Memory limit | Running Pod specification | The actual container memory ceiling, not the form's requested value. |
| Pod traffic | Non-loopback `/proc/net/dev` counters | Received and transmitted bytes per second across the Pod network. Includes sidecar traffic and excludes loopback. Two readings are required. Pod changes, counter resets, and collection gaps restart the baseline. |
| Data filesystem | `df` at `/home/steam/rsdw-dedicated` | Used bytes and capacity of the filesystem backing the data mount. On kind, this may be a shared filesystem. It is not world-save size or a PVC quota. |
| Tick rate | Authenticated game `/api/metrics` | Completed UDomGameEngine::Tick calls divided by the measured window. Requires the tick-enabled rsdw_api build. |
| Tick duration p50/p95/p99 | Authenticated game `/api/metrics` | Nearest-rank percentiles of elapsed wall time inside the game engine tick. Includes its nested engine/world tick and synchronous delegates. Excludes work outside that call. |
| Tick window and sample count | Authenticated game `/api/metrics` | Actual elapsed window and number of completed calls used for both rate and percentiles. |

Each metric has its own status, source, observation timestamp, and failure reason. The statuses are `available`, `stale`, `error`, `unavailable`, `unsupported`, `warming_up`, and `disabled`. A missing Metrics API does not suppress player data. A real zero is displayed as zero. An expired observation is stale and is not presented as current.

Live server responses omit the old flat numeric metric fields. API clients must read the nullable `metrics` map. `metricsAvailable` means at least one reading is available, not that every metric is available. Demo records without a metric map retain their legacy fields.

Charts use observation timestamps and preserve gaps. Their expandable tables provide the numeric data without requiring visual chart inspection. CSV export includes metric names, values, source timestamps, units, sources, and availability.

Metrics Server provides CPU and memory, not historical storage, network throughput, disk usage, or game ticks. The kind startup script installs a pinned Metrics Server release. See [Kubernetes resource metrics](https://kubernetes.io/docs/tasks/debug/debug-cluster/resource-metrics-pipeline/) and [Metrics Server requirements](https://kubernetes-sigs.github.io/metrics-server/).

The tick-enabled rsdw_api library wraps the verified UDomGameEngine tick at process startup. It changes a private in-memory function pointer, not Unreal or shipped game files. Support is limited to verified executable builds. An unknown build, absent endpoint, failed collection, or stale producer returns no numeric tick measurements. Configured frame limits, CPU consumption, and readiness are not substitutes for measured ticks.

The producer retains up to 8192 completed calls in a ten-second lookback. A preceding completion anchors the actual elapsed window. Warmup requires an anchor and at least one second of observations. Long calls remain eligible when they complete. More than two seconds without a completion makes the producer stale. A complete-call window that exceeds storage capacity remains unavailable until the gap ages out.

C2 checks the producer timestamp, age, source, scope, window arithmetic, and percentile order. Accepted snapshots use the tick source's freshness window (three tick intervals, at least 45 seconds) and retain their original observation timestamps. The snapshot is an instantaneous measurement, so one poll per interval shows a point, not a continuous series. Each chart point contains that snapshot's percentiles. The dashboard does not average them into a percentile across the selected chart range.

The implementation and verification boundaries are recorded in [tick measurement design](../verification/tick-design.md) and [the actual-server verification report](../verification/tick-verification.md). The acceptance checklist is in [issue #3](https://github.com/petzkod5/rsdw-c2/issues/3).
