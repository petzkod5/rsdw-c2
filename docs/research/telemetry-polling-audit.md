# Telemetry polling audit and tiered-collection proposal

Status: research only. No product code changed. Code references are to commit `ee2746e` (branch base).

## 1. Question

The owner suspects the console's background telemetry polling causes in-game lag. This is unproven. This document (a) enumerates exactly what the collector does to each game server and how often, (b) says what the evidence does and does not support, and (c) proposes a cheaper, configurable, tiered design that keeps ONLINE / OFFLINE / STOPPED reliable, plus a measurement plan that can prove or disprove the hypothesis.

## 2. Key numbers

Defaults: 15 s interval, 1 server, a running pod with a LoadBalancer service that already has an ingress address.

| Quantity | Per cycle (per server) | Per minute (per server) | Per minute, 3-server fleet |
| --- | --- | --- | --- |
| `kubectl exec` into the game container | **5** | **20** | **60** |
| Game mod API HTTP requests (all via exec+curl) | 3 (`health`, `players`, `metrics`) | 12 | 36 |
| Short-lived processes started inside the game container (estimate, see 3.3) | about 11 | about 44 | about 132 |
| `kubectl get` calls (console to API server, no game impact) | 6 (7 with the `get nodes` fallback) | 24 (28) | 72 (84) |
| Total `kubectl` processes spawned in the console pod | 11 (12) | 44 (48) | 132 (144) |
| `kubectl exec` per hour | 300 | 1,200 | 3,600 |

Findings in one paragraph: the console never calls the game server from a browser request; the browser only reads an in-memory cache. All game-facing load comes from one background loop, `runCollector` at 15 s. Every cycle that loop runs five `kubectl exec` commands inside the game container, one of which is the only source of the liveness signal (`/api/health`), so cheap liveness and expensive metrics are currently coupled. Whether this causes visible lag is unproven: the load per exec is small, but the churn is continuous, exec'd processes are charged to the game container's cgroup (CPU quota), and the processes likely inherit `LD_PRELOAD` of the `rsdw_api` mod library (section 6). The measurement plan in section 9 decides it.

## 3. Collector audit

### 3.1 Scheduling

| Item | Value | Where |
| --- | --- | --- |
| Loop start and interval | `go app.runCollector(ctx, 15*time.Second)`; hard-coded, not configurable | `main.go:1719` |
| Loop behaviour | `collectTelemetry`, then wait for next tick. `time.Ticker` drops ticks if a cycle overruns, so effective interval is max(15 s, cycle duration) | `telemetry.go:146-157` |
| Overlap guard | `collecting.TryLock()`; an overlapping call returns immediately | `telemetry.go:161` |
| Concurrency | `min(4, len(servers))` worker goroutines, one server per worker | `telemetry.go:173-175` |
| Per-server budget | 12 s total (`context.WithTimeout`), sources get 12 s minus 1.5 s | `kube_telemetry.go:375, 418-420` |
| Per-command timeout | 1.5 s (`telemetryCommandTimeout`) for every `kubectl get` and `kubectl exec` | `kube_telemetry.go:21, 103, 328` |
| `resolvePod` timeout | 3 s for its three `get` calls combined | `kube_telemetry.go:226` |
| Cache freshness | `telemetryMaxAge` 45 s; readings older than that become `stale`, observation older than that makes status `unknown` | `telemetry.go:12, 110-119, 296-299` |
| Retention | 1 h, max 240 samples per server (240 x 15 s = 1 h exactly) | `telemetry.go:13-14, 325-343` |
| `tickMaxAge` | 2 s: a game tick snapshot older than 2 s at collection time is rejected as stale; `tickTransportLimit` 1.5 s | `telemetry.go:15-16`, `kube_telemetry.go:756` |
| Worker also does | alert evaluation, memory-pressure evaluation, optional restart dispatch, under `lifecycleMu.TryLock()`; a failed lock silently drops the observation | `telemetry.go:207-247` |

### 3.2 Every request per server per cycle

Order is as executed in `collectObservation` (`kube_telemetry.go:374-487`). All steps are sequential inside one server's worker.

| # | Step | Mechanism | Target | Touches game container? | Needed for status? | Source |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Deployment | `kubectl get deployment -o json` | API server | No | **Yes**: `replicas == 0` means STOPPED | `kube_telemetry.go:234, 240-242` |
| 2 | ReplicaSets | `kubectl get replicasets -o json` (whole namespace) | API server | No | Yes (ownership) | `kube_telemetry.go:248` |
| 3 | Pods | `kubectl get pods -o json` (whole namespace) | API server | No | **Yes**: container running + Ready | `kube_telemetry.go:263, 316-324` |
| 4 | Service endpoint | `kubectl get service -o json`, plus `kubectl get nodes -o json` for NodePort/LB without ingress address | API server | No | No (endpoint display) | `kube_telemetry.go:148-223, 390` |
| 5 | Metrics API | `kubectl get --raw /apis/metrics.k8s.io/v1beta1/namespaces/N/pods/P` | API server, metrics-server | No (data already collected by kubelet/cAdvisor out of band) | No | `kube_telemetry.go:520-584` |
| 6 | `/api/health` | `kubectl exec ... -- sh -ec 'curl -fsS --max-time 3 -H "Authorization: Bearer $(cat /run/rsdwapi/token)" http://127.0.0.1:8080/api/health'` | **Inside game container** | **Yes** | **Yes**: `engineReady` gates ONLINE | `kube_telemetry.go:351-365, 588-626` |
| 7 | `/api/players` | same exec+curl wrapper, `--max-filesize 262144` | Inside game container | Yes | No | `kube_telemetry.go:361, 627-646` |
| 8 | Disk | `kubectl exec ... -- env LC_ALL=C df -Pk -- <mount>` | Inside game container | Yes | No | `kube_telemetry.go:786` |
| 9 | Network | `kubectl exec ... -- cat /proc/net/dev` | Inside game container | Yes | No | `kube_telemetry.go:427` |
| 10 | `/api/metrics` (tick) | same exec+curl wrapper | Inside game container | Yes | No | `kube_telemetry.go:678-691` |
| 11 | Identity re-check | `kubectl get pod <name> -o json` | API server | No | **Yes**: discards the whole cycle if the pod changed | `kube_telemetry.go:444-457` |

Counts: 5 execs (steps 6-10) plus 6 gets (1, 2, 3, 4, 5, 11), 7 when step 4 needs `get nodes`. Steps 1-3 and 11 are the real liveness inputs; step 6 supplies `engineReady`.

Steps 7 and 10 each run a fresh `sh`, a command substitution (`cat` of the token) and `curl`. Steps 6, 7 and 10 therefore start roughly three short-lived processes each inside the game container; steps 8 and 9 start one each (`env` replaces itself with `df`). Hence about 11 processes per cycle. This is an estimate: `sh` may exec the final command directly depending on the shell (dash/bash version in the image); it was not measured.

### 3.3 What can hurt the game

- Each `kubectl exec` is: kubectl process in console pod, then API server, then kubelet streaming server, then CRI runtime (`runc exec`), which joins the container's namespaces **and cgroup**. The new process runs under the container's CPU quota and memory limit. This is general runtime behaviour; the sources found for this audit show cgroups enforcing per-container limits but none states the exec case outright, so it is **[inference, to be verified in section 9]**.
- Under CFS bandwidth control the quota is per cgroup (default 100 ms period): once exhausted, every thread in that cgroup waits for the next period ([kernel docs](https://docs.kernel.org/scheduler/sched-bwc.html)). The game container's default CPU limit is 1000m (`main.go:700-701`), so the budget is 100 ms of CPU per 100 ms period across all Unreal threads. Spawning 11 processes plus the runtime's exec setup every 15 s consumes a few tens of ms of CPU in bursts; whether that pushes a busy tick over the quota is an empirical question, and is most likely when the game is already near its limit.
- Throttling is invisible in average-CPU dashboards: one source reports a pod at 12% average CPU throttled 60% of the time ([jorijn.com](https://jorijn.com/en/knowledge-base/kubernetes/monitoring/kubernetes-cpu-throttling-pods-stall-low-utilisation/)). This is why the console's own CPU tiles (Metrics API average) cannot confirm or deny the hypothesis.
- Timeout asymmetry: the exec has a 1.5 s client timeout, but the in-container `curl` has `--max-time 3` (`kube_telemetry.go:363`). Killing the local `kubectl` on timeout ends the client; sources report the in-container process may survive the disconnect ([codemia write-up](https://codemia.io/knowledge-hub/path/timeout_for_kubectl_exec), secondary source). So a slow mod API (for example during a long game hitch) can leave a curl and its shell behind for up to 3 s per overlapping cycle. Low risk, but worth checking with `ps` during a test.
- A slow game is the worst case for the probe and the probe is then at its most intrusive: a stalled `/api/health` costs 1.5 s of waiting and the remaining steps are still attempted sequentially (they only share the 10.5 s source deadline). A failing mod API does not skip steps 7-10.
- **`LD_PRELOAD` inheritance (likely, unverified).** The tick producer lives in the preloaded `rsdw_api` library (`verification/tick-design.md:3`). The repo's own verification scripts run `docker exec -e LD_PRELOAD= ... curl` (`verification/check-live-ticks.py:23`, `verification/check-tick-restart.py:14`), i.e. the authors explicitly cleared the variable for exec'd helpers. The production collector does **not** do this (`kube_telemetry.go:334, 363`). If `LD_PRELOAD` comes from the container's environment, every exec'd `sh`, `cat`, `curl` and `df` loads the mod library and runs its initialisers (possibly symbol resolution, thread start, socket bind attempts) in a throwaway process. That would multiply the per-exec cost and could, in the worst case, contend with the real instance's state. This needs a direct check (`kubectl exec <pod> -c server -- env | grep LD_PRELOAD`, and watch the library's startup log lines per exec).

Not a game-side risk: the Metrics API read (step 5), the `get` calls, and `kubectl logs` do not execute anything in the game container. They do cost the console pod CPU: 44 `kubectl` forks per minute per server against a console CPU limit of 250m (`charts/rsdw-c2/values.yaml:76`), which is a separate scaling concern.

### 3.4 How status is derived today (the part that must stay reliable)

- `resolvePod` alone already produces the lifecycle states from Kubernetes only: STOPPED (replicas 0, `errScaledZero`), `starting` (no active pod, or container not running/Ready), `online` (container Ready) (`kube_telemetry.go:240-242, 281-285, 316-324`).
- `collectObservation` then **overrides** that with the game API result: `engineReady == 1 && container ready && pod Ready` gives `online`; `engineReady == 0` gives `starting`; **engineReady missing (health exec failed) while Kubernetes said online gives `attention`** (`kube_telemetry.go:470-480`).
- If the final identity re-check (step 11) fails or the pod changed, **every metric is wiped and status becomes `unknown`** (`kube_telemetry.go:443-456`).
- `joinObservation` makes status `unknown` when the newest observation is older than 45 s (`telemetry.go:294-299`).
- There is no literal `offline` status. The user-facing OFFLINE corresponds to `attention`/`unknown` today (`main.go:62-71`).

So today a single 1.5 s exec timeout on `/api/health` (a transient lost websocket, a busy kubelet, a game hitch) flips a healthy server from ONLINE to ATTENTION for one cycle, and the dashboard shows "Check failed" tiles (`web/app.js:646`, shown when `status === 'unknown'` and the reading is `unavailable` with a reason). The liveness signal and the expensive metrics share one fate.

## 4. Browser side

| Item | Finding | Where |
| --- | --- | --- |
| Refresh cadence | `setInterval(..., 10000)`; skipped when paused, hidden, a modal is open, or an input/select has focus; also on tab becoming visible | `web/app.js:2267-2275` |
| Requests per refresh | `/api/auth/*` discovery, `GET /api/bootstrap`, `GET /api/events`, optional `/api/users`, `/api/integrations`, `/api/reboots`, `/api/backups`, and on the Telemetry/Servers page `GET /api/servers/:id/telemetry?range=...` | `web/app.js:1319-1372` |
| Logs | On the Telemetry page, `loadLogs` is also called each refresh (`kubectl logs deployment/... --tail N`, a kubelet log read, 10 s timeout). This is the only browser-driven call that reaches the cluster. It is not an exec | `web/app.js:1371`, `main.go:854-859, 1355-1366` |
| Does the UI trigger live collection? | **No.** `handleBootstrap` and the telemetry route call `telemetryFor`, which only reads `cache.history` (`telemetry.go:359-430`; no orchestrator call). `Refresh`/`CheckUpdate` (`main.go:862, 877`) run `resolvePod` + service lookup only on explicit admin actions, never from the poll | `main.go:1090, 1370` |
| Multiple tabs | They multiply HTTP requests to the console (every open tab, 6 refreshes per minute) and, on the Telemetry page, `kubectl logs` calls (6/min per tab per server). They do **not** multiply `kubectl exec` or mod-API calls | as above |

Caveat on UI timing: the UI polls every 10 s but the cache changes every 15 s, so roughly one in three UI refreshes shows identical data. Not a game concern.

## 5. Other things that touch the game pod

| Component | Cadence | Touches game container? | Where |
| --- | --- | --- | --- |
| `alerts.go` (`observeAlerts`) | Runs inside the collector worker per observation | No. Consumes the cached observation | `alerts.go:66`, `telemetry.go:228` |
| `memory_pressure.go` (`observe`) | Same | No. Consumes `memoryUsedBytes` from the Metrics API reading | `memory_pressure.go:42-88` |
| `lifecycle.go` | On demand (`Scale 0/1`) | No polling | `lifecycle.go:126, 145` |
| Reboot scheduler | 15 s tick | No exec; schedules only. Executes restarts via orchestrator when due | `reboots.go:18, 930` |
| Backup scheduler | 15 s tick, but acts only when a schedule is due; a backup runs `resolvePod` and `kubectl exec ... tar`/`cat` streaming from the game container (heavy but occasional, and user-configured) | Only when a backup runs | `backups_app.go:496`, `backups_kube.go:111, 139, 395, 399` |
| Deletion reconciler | 5 s tick, only for servers being deleted | N/A | `deletion.go:30, 292` |
| Seed cleanup | 1 min tick | Execs into the temporary `writer` pod, not the game pod | `save.go:241, 387` |
| Discord delivery | 1 s tick | No cluster access | `discord.go:231` |

Conclusion: the collector is the only continuous source of load on the game container. Backups are the only other source that executes in it.

## 6. What the outside evidence says

- A CPU limit is enforced as a per-cgroup CFS quota, default period 100 ms; exceeding it stalls the group's threads until the next period. Counters: `nr_throttled`, `throttled_time` ([kernel scheduler docs](https://docs.kernel.org/scheduler/sched-bwc.html)). Multithreaded workloads are throttled earlier than their average CPU suggests ([Red Hat](https://access.redhat.com/node/5285071), [last9](https://last9.io/blog/kubernetes-cpu-throttling), [cloudoptimo](https://cloudoptimo.com/blog/kubernetes-cpu-throttling-cfs-quotas-and-latency-fixes/)). I found no Unreal-specific source that documents hitching from this pattern; the link is a plausible mechanism, not an established fact.
- Whether exec'd processes are charged to the container cgroup: general container-runtime behaviour (runc places exec processes in the container's cgroup), but none of the search results states it directly. Treat as to-verify.
- Exec probes are discouraged for latency-sensitive workloads: Red Hat advises against exec probes in resource-constrained, low-latency environments ([Red Hat probe design blog](https://cloud.redhat.com/blog/how-to-design-probes-for-telco-low-latency-workloads-in-openshift)). Kubernetes' own `httpGet` probe runs from the kubelet with no in-container process.
- Metrics Server samples kubelet/cAdvisor out of band; its documented resolution is 60 s by default (adjustable, not recommended below the kubelet's 15 s) ([metrics-server FAQ mirror](https://github.com/dgrisonnet/metrics-server/blob/master/FAQ.md); a study guide gives 15 s, so the default depends on version). Polling the Metrics API every 15 s therefore often re-reads an unchanged sample but is cheap for the game. Our code already rejects readings older than 45 s (`kube_telemetry.go:546`).
- Other panels separate liveness from expensive work. Pterodactyl Wings streams Docker stats over websocket about once a second (a Docker-side stat read, no in-container process) and computes disk usage on a separate, slow timer, `disk_check_interval`, default 150 s, with documentation warning that low values cause heavy I/O and CPU ([Wings system settings](https://www.mintlify.com/pterodactyl/wings/configuration/system-settings), [monitoring](https://www.mintlify.com/pterodactyl/wings/operations/monitoring); these are third-party mirrors of the Wings docs, not pterodactyl.io itself; the canonical docs page is [pterodactyl.io/wings/1.0/configuration](https://pterodactyl.io/wings/1.0/configuration)). I could not find usable published polling intervals for PufferPanel or AMP; I make no claim about them.
- Takeaway used in the proposal: the established pattern is host-side stats (cgroup/Docker/kubelet) for resource numbers, no per-sample process inside the game, and a much longer timer for disk.

## 7. Findings

1. **Backend only.** UI load never reaches the game. All game-side load is `runCollector` at a hard-coded 15 s (`main.go:1719`).
2. **5 execs per server every 15 s, with about 11 short-lived processes in the game container**, i.e. 20 execs/min/server and 60/min for 3 servers. Nothing is configurable.
3. **Liveness and metrics are coupled.** `engineReady` comes from an exec'd curl that runs in the same sequence as four optional metric execs, with a shared 10.5 s deadline. A transient failure there flips ONLINE to ATTENTION; a failed final `get pod` flips to UNKNOWN and wipes all metrics.
4. **Three of the five execs have a host-side alternative** that does not run anything in the game container: CPU and memory already come from the Metrics API; network counters could come from kubelet/cAdvisor (`container_network_*`), not from `cat /proc/net/dev`; disk used/capacity could come from the kubelet stats summary or PVC metrics (`kubelet_volume_stats_*`). Disk changes slowly and needs minutes, not 15 s.
5. **The two game API calls that matter (health, players) are legitimate but could be one call or a mod-side push.** `/api/health` is the only exec needed for a reliable engine-ready signal today. A Service-level probe (kubelet `readinessProbe` of type httpGet) already exists conceptually via pod `Ready`; if the chart's readiness probe covers the mod API, even `/api/health` from the console is redundant for ONLINE (see 8.1).
6. **The lag hypothesis is plausible but unproven.** The mechanism (CFS quota shared with exec'd processes, possible `LD_PRELOAD` inheritance) is credible and testable; the measured effect is unknown. Section 9 gives a falsifiable plan.
7. **Worker contention risk.** If `lifecycleMu` is held by a backup or restart, `TryLock` fails and the observation is dropped (`telemetry.go:207-209`); the loop continues exec'ing but loses the result. Status then ages to `unknown` after 45 s. Liveness must not depend on that lock.

## 8. Proposal

### 8.1 Two tiers

**Tier 1: liveness (cheap, always on).** Goal: reliable ONLINE / OFFLINE / STOPPED.

| Input | Source | Game container impact |
| --- | --- | --- |
| Replicas == 0 | Deployment `get` | None |
| Pod exists, owned, Running | `get pods` | None |
| Container Ready and Pod Ready conditions | Pod status (already includes the chart's readiness probe) | None |
| Pod identity unchanged (UID, containerID, startedAt) | Same pod object read, no separate trailing `get` | None |
| Engine readiness (optional, kept) | `/api/health` | One exec-curl (see below) |

Reduce the API chatter: replace per-server `get deployment` + namespace-wide `get replicasets` + `get pods` with one namespace-scoped list of pods and deployments per namespace per cycle (shared across all servers in that namespace), or a `get ... -l` with label selectors. Drop the separate final `get pod` by reading the Pod once and validating (UID, containerID, startedAt) from that object; re-fetch only if a mod API result was gathered.

For `/api/health`, two options in order of preference:

1. **Make the pod's own readiness probe the engine-ready signal** (chart change: `readinessProbe` httpGet on the mod API `/api/health`, no auth needed or with a probe-only path). Then ONLINE = `Ready` from the Kubernetes API with zero execs. The kubelet probes without a shell in the container. This is the preferred end state but requires a game-chart change and a small mod/API contract confirmation (out of scope for this repo alone; the chart is `RSDW_CHART`, `main.go:633`).
2. **Keep `/api/health` via exec** but as the only exec in Tier 1: one `curl` against localhost, no `sh -ec`/`cat` pipeline where avoidable (read the token once into the console, pass it as an argument, or use a small static helper), `env -u LD_PRELOAD` (or `LD_PRELOAD=`) so the helper does not load the mod library, bounded by `timeout 2`. Interval: default 15 s as today (configurable, see 8.2), with backoff when the pod is not Ready (no point polling a pod Kubernetes says is not Ready).

Liveness runs on its own loop and never waits for Tier 2. Its observation is merged into the cache as `status`, `runtime`, `health`, `engineReady`, `uptimeSeconds`, pod image/endpoint. It must not hold `lifecycleMu` for longer than the in-memory update (alerts/restart dispatch remain on this path, as they depend on status).

**Tier 2: metrics (expensive, slower, individually toggleable).**

| Metric group | Source | Default interval | Notes |
| --- | --- | --- | --- |
| CPU, memory (Metrics API) | `get --raw /apis/metrics.k8s.io/...` | 30 s | No game impact; memory-pressure policy needs fresh samples, so memory stays at most 30 s |
| Players + roster | `/api/players` (exec-curl) | 30 s | Alerts (player joined) tolerate 30 s; make the interval fixed to what alert logic documents |
| Tick | `/api/metrics` (exec-curl) | 15 s default, but **off by default**? see below | Tick data is only valid for 2 s (`tickMaxAge`); a 15-60 s poll shows an instantaneous snapshot, not a continuous series. Value for lag diagnosis is high; cost is one exec-curl |
| Network | `cat /proc/net/dev` (exec) | 60 s | Prefer kubelet/cAdvisor counters when available |
| Disk | `df` (exec) | 300 s | Mirrors Pterodactyl's 150 s disk timer; disk changes slowly |

Reason for the intervals: any metric that needs an exec costs a process in the game container, so each exec metric is its own toggle and its own timer; metrics that do not need an exec (Metrics API) can stay cheap. When every exec metric is off, the only exec left is Tier 1 `/api/health`, i.e. 4 execs/min/server, a 5x reduction from today, and 0 if the readiness-probe option is adopted.

### 8.2 Configuration (env and Helm)

Follow the existing pattern of `RSDW_MEMORY_PRESSURE_*` (`memory_pressure.go:24-40`, `charts/rsdw-c2/templates/deployment.yaml:80-85`, `values.yaml`, `values.schema.json`).

| Env var | Helm value | Default | Bounds | Meaning |
| --- | --- | --- | --- | --- |
| `RSDW_TELEMETRY_LIVENESS_INTERVAL` | `telemetry.livenessInterval` | `15s` | 5s - 5m | Tier 1 loop |
| `RSDW_TELEMETRY_METRICS_INTERVAL` | `telemetry.metricsInterval` | `30s` | 10s - 15m | Base for Tier 2 Metrics API and players |
| `RSDW_TELEMETRY_EXEC_METRICS_ENABLED` | `telemetry.execMetrics.enabled` | `true` | bool | Master switch for all exec-based metrics (network, disk, tick, and players-over-exec) |
| `RSDW_TELEMETRY_PLAYERS_ENABLED` | `telemetry.players.enabled` | `true` | bool | Game `/api/players` |
| `RSDW_TELEMETRY_TICK_ENABLED` | `telemetry.tick.enabled` | `true` | bool | Game `/api/metrics` (requires exec metrics) |
| `RSDW_TELEMETRY_NETWORK_ENABLED` | `telemetry.network.enabled` | `true` | bool | `/proc/net/dev` (requires exec metrics) |
| `RSDW_TELEMETRY_DISK_ENABLED` | `telemetry.disk.enabled` | `true` | bool | `df` (requires exec metrics) |
| `RSDW_TELEMETRY_DISK_INTERVAL` | `telemetry.disk.interval` | `5m` | 1m - 1h | Disk poll |
| `RSDW_TELEMETRY_NETWORK_INTERVAL` | `telemetry.network.interval` | `1m` | 15s - 15m | Network poll |
| `RSDW_TELEMETRY_TICK_INTERVAL` | `telemetry.tick.interval` | `30s` | 10s - 15m | Tick poll |

Rules the implementer should enforce:
- Parse in `main.go` next to `memoryPressurePolicyFromEnv` (same invalid-value fatal at startup); durations are Go durations; reject outside bounds with the variable name in the error.
- Freshness: any metric interval must be at most `telemetryMaxAge / 1.5`, i.e. 30 s, **or** `telemetryMaxAge` must be derived per tier (for example 3 x the metric's interval, minimum 45 s) so slower metrics do not constantly render as `stale` (`telemetry.go:110-119`). The current constant is hard-wired into `freshMetrics`, `joinObservation`, alerts and memory pressure, so this is the central code change; keep liveness at 45 s and give metrics a per-key max age.
- Retention: `telemetryMaxSamples` (240) assumes 15 s samples (`telemetry.go:14`). Keep time-based retention (1 h) and size the cap from the slowest intended interval or separate sample streams; do not store a 60 s metric as a 15 s sample.
- Expose the effective, validated values read-only in `GET /api/integrations` or a new `GET /api/settings/telemetry` and render them in the Integrations/Settings page as a "Telemetry polling" card (liveness interval, metrics interval, each toggle, estimated execs per minute per server computed from the config). No write path in the UI: changing them is a Helm upgrade. Add the same fields to the RBAC-aware capability so viewers see nothing.
- Add `telemetry` to `values.schema.json` `required`/`properties` the way `memoryPressure` is wired (`values.schema.json:5, 18`).
- Docs: update `docs/telemetry.md` ("The dashboard schedules collection every 15 seconds") and `verification/telemetry-design.md:3`.

### 8.3 Behaviour when metrics fail but liveness succeeds

- Status stays **ONLINE** (`online`) whenever Tier 1 says Ready and `engineReady == 1`. Tier 2 results must never feed back into `status`, `health`, or the `unknown` wipe (`kube_telemetry.go:443-457`): that all-metrics wipe should be limited to the metrics it actually protects and must not change status.
- The `engineReady == nil` to `attention` rule (`kube_telemetry.go:478-480`) stays, but for Tier 1 only, and requires **two consecutive** failed health probes (or the pod `Ready` condition false) before leaving ONLINE; one timeout is not an outage.
- Metric tiles show `Collecting` (warming), `Awaiting sample`, or `Unavailable` and a dash, never `Check failed` or `Collection failed` on the dashboard. `metricText` (`web/app.js:641-648`) currently returns `Check failed` when `server.status === 'unknown'` and reasons exist; that string should only appear if the **status itself** is `unknown`. When status is `online` and a metric is `error`/`stale`, render `Unavailable` (neutral) and keep the reason in the Telemetry page detail, with the failure visible there rather than as an alarm on the dashboard.
- Disabled-by-config metrics render as `Disabled` (new reading status `disabled`, reason "Turned off by configuration") and do not count towards `metricsAvailable` or alerts.
- Alerts: `player joined`, memory-pressure restart and others must treat missing/stale metrics as "no new information", as they do today (`alerts.go:121-144`, `memory_pressure.go:50-56`); verify with tests that disabling exec metrics does not trigger outage or restart events.
- STOPPED must come only from `replicas == 0` (Kubernetes), never from a failed probe.

### 8.4 Suggested implementation order for the worker

1. Add the config struct and env parsing + Helm values/schema + tests (no behaviour change; defaults reproduce today's 15 s for everything).
2. Split `collectObservation` into `collectLiveness` and per-group `collect*` functions with their own next-due timestamps; keep results in the observation but merge per key with their own `ObservedAt`.
3. Change freshness to per-key max age; keep liveness at 45 s.
4. Drop the redundant final `get pod` and collapse `deployment`/`replicasets`/`pods` fetches per namespace per cycle.
5. Clear `LD_PRELOAD` and bound with `timeout` on helper execs; read the token once per cycle rather than `sh -ec` + `cat`.
6. Status/UI rules from 8.3 and the read-only Settings card.
7. Docs and the measurement harness (section 9).

## 9. Proving or disproving the lag hypothesis

Goal: decide whether exec polling measurably affects the game, before and after turning off exec metrics, on the e2e (kind) environment or a real node.

### 9.1 Signals

| Signal | How | Why |
| --- | --- | --- |
| CFS throttling of the game container | Prometheus/cAdvisor: `rate(container_cpu_cfs_throttled_periods_total{container="server"}[1m]) / rate(container_cpu_cfs_periods_total{container="server"}[1m])`, plus `container_cpu_cfs_throttled_seconds_total`. Without Prometheus: read `cpu.stat` (`nr_periods`, `nr_throttled`, `throttled_usec`) from the container cgroup on the node | Direct evidence that something steals quota |
| Tick latency | Existing `tickP50Ms`/`tickP95Ms`/`tickP99Ms`/`tickRate` from the mod (`telemetry.go:19`). p99 and the maximum tick are the lag proxies | The most game-relevant measurement already in the product |
| Health response latency | Time `curl -w '%{time_total}' /api/health` at 1 Hz from a side process or from the console (log `received.Sub(started)` as in `kube_telemetry.go:679-681`) | Cheap, independent of exec polling if issued from outside the container |
| Process churn | `ps`/`pidstat` or `/proc/<pid>/cgroup` listing in the container: count of exec'd processes per minute, and presence of leftover `curl`/`sh` after timeouts | Confirms section 3.3 estimates |
| Exec cost | CPU time (`cpu.stat` usage_usec delta) of the game cgroup over a window with polling on vs off, idle server | Quantifies cost with zero players |
| `LD_PRELOAD` | `kubectl exec <pod> -c server -- env \| grep LD_PRELOAD` and whether `rsdw_api` initialisation logs appear once per exec | Confirms/denies library reload per exec |

### 9.2 Experiment design

Use the same world, same node, a CPU limit tight enough to expose throttling (the default 1000m, and a second run at 500m as a stress condition), and a synthetic load that approximates players (a bot or a recorded load; if none, run idle and with the mod's `players` fixture, but then state clearly that the result applies to idle servers only).

Run A/B in alternating blocks of 10 minutes (ABABAB) to cancel drift; each arm 30+ minutes total:

- **Arm A (baseline):** current behaviour, 15 s, all execs.
- **Arm B (exec off):** `RSDW_TELEMETRY_EXEC_METRICS_ENABLED=false`; liveness only (or, before the feature exists, temporarily set a build flag/patch to skip steps 7-10; the instrumented binary is a throwaway).
- **Arm C (liveness only via readiness probe):** zero execs, once the chart change exists.
- **Arm D (stress):** tick-level p99 at 500m CPU limit, Arm A vs Arm B.

Decision rule (pre-registered):
- Disproved if the difference in throttled-period ratio between A and B is under 1 percentage point and the tick p99 difference is within the run-to-run noise (estimate noise from the first two baseline blocks).
- Supported if A shows a recurring throttling or p99 spike aligned to the 15 s cycle (autocorrelation at 15 s, or spikes within 1 s after each cycle start) that disappears in B.
- If A shows spikes but B still does, look elsewhere (metrics-server scrape, backups, GC), and re-run with `kubectl exec` disabled entirely and an external HTTP probe.

### 9.3 Automation for the e2e environment

- Add a `scripts/measure-telemetry-impact.sh` that, for N minutes, samples `cpu.stat` (through the node or `kubectl exec ... cat /sys/fs/cgroup/cpu.stat` from a single, non-polling process at low frequency), the tick endpoint, and the health latency, writes CSV, and prints the A/B summary. The sampler must itself be light and tagged so it can be excluded from the analysis.
- Wire it into the kind e2e (`scripts/kind-smoke.sh`, `docs/kind.md`) as an opt-in step. kind runs on Docker so the cgroup path differs (cgroup v2 on the kind node); run the sampler on the node container via `docker exec <kind-node> cat /sys/fs/cgroup/kubelet.slice/.../cpu.stat`.
- Store results next to existing verification reports (`verification/`), with the raw CSV.

## 10. Risks and open questions

- Whether `LD_PRELOAD` is inherited by exec'd processes was not verified (needs a live pod). If true, step 5 of 8.4 alone may remove most of the per-exec cost.
- Exact process count per exec depends on the shell in the game image; measure.
- Moving `engineReady` to the readiness probe requires a chart and possibly a mod change (the game chart is external, `RSDW_CHART`, `main.go:633`). The proposal works without it (Tier 1 with one exec).
- kubelet/cAdvisor-based network and disk metrics require RBAC for `nodes/proxy` or `nodes/stats` or a Prometheus dependency; this is optional future work. The current RBAC already grants `nodes` get/list and `metrics.k8s.io` pods (`charts/rsdw-c2/templates/rbac.yaml:9-14`) but not `nodes/stats`.
- The `lifecycleMu.TryLock()` drop-on-contention (`telemetry.go:207`) is orthogonal but affects liveness freshness during backups and restarts; split Tier 1's cache write from alert/restart processing so a held lock cannot age out status.
- Search coverage: I found no first-party or Unreal-specific source on exec-induced hitching, and no usable PufferPanel/AMP polling documentation. Claims about those are explicitly absent.

## 11. Sources

- Linux kernel, CFS bandwidth control: https://docs.kernel.org/scheduler/sched-bwc.html
- Kubernetes CPU throttling at low utilisation: https://jorijn.com/en/knowledge-base/kubernetes/monitoring/kubernetes-cpu-throttling-pods-stall-low-utilisation/
- Red Hat, CFS quotas can lead to unnecessary throttling: https://access.redhat.com/node/5285071
- last9, Kubernetes CPU throttling: https://last9.io/blog/kubernetes-cpu-throttling
- CloudOptimo, CFS quotas and latency: https://cloudoptimo.com/blog/kubernetes-cpu-throttling-cfs-quotas-and-latency-fixes/
- Red Hat, designing probes for low-latency workloads (advises against exec probes): https://cloud.redhat.com/blog/how-to-design-probes-for-telco-low-latency-workloads-in-openshift
- Codemia, `timeout kubectl exec` (client timeout does not kill the remote process; secondary): https://codemia.io/knowledge-hub/path/timeout_for_kubectl_exec
- metrics-server FAQ (resolution): https://github.com/dgrisonnet/metrics-server/blob/master/FAQ.md
- Pterodactyl Wings system settings (`disk_check_interval`, third-party mirror): https://www.mintlify.com/pterodactyl/wings/configuration/system-settings
- Pterodactyl Wings monitoring (third-party mirror): https://www.mintlify.com/pterodactyl/wings/operations/monitoring
- Pterodactyl Wings configuration (official): https://pterodactyl.io/wings/1.0/configuration
- Repo evidence: `telemetry.go`, `kube_telemetry.go`, `main.go`, `web/app.js`, `alerts.go`, `memory_pressure.go`, `lifecycle.go`, `backups_kube.go`, `verification/tick-design.md`, `verification/check-live-ticks.py`.
