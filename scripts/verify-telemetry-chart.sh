#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
chart=(charts/rsdw-c2 --set auth.adminTokenSecret.name=console-admin)
check_env() {
  local rendered="$1" name="$2" value="$3"
  [[ "$rendered" == *"name: $name"$'\n'"              value: \"$value\""* ]]
}
absent_env() {
  [[ "$1" != *"name: $2"$'\n'* ]]
}
rendered=$(helm template telemetry-test "${chart[@]}")
check_env "$rendered" RSDW_TELEMETRY_LIVENESS_INTERVAL 15s
check_env "$rendered" RSDW_TELEMETRY_METRICS_INTERVAL 60s
check_env "$rendered" RSDW_TELEMETRY_KUBE_TIMEOUT 5s
check_env "$rendered" RSDW_TELEMETRY_HEALTH_TIMEOUT 4s
check_env "$rendered" RSDW_TELEMETRY_EXEC_TIMEOUT 4s
check_env "$rendered" RSDW_TELEMETRY_EXEC_METRICS_ENABLED true
check_env "$rendered" RSDW_TICK_METRICS false
for source in RESOURCES PLAYERS NETWORK DISK; do
  check_env "$rendered" "RSDW_TELEMETRY_${source}_ENABLED" true
done
check_env "$rendered" RSDW_TELEMETRY_DISK_INTERVAL 5m
absent_env "$rendered" RSDW_TELEMETRY_TICK_INTERVAL
absent_env "$rendered" RSDW_TELEMETRY_NETWORK_INTERVAL
rendered=$(helm template telemetry-test "${chart[@]}" --set telemetry.livenessInterval=30s --set telemetry.metricsInterval=2m --set telemetry.execMetrics.enabled=false --set telemetry.players.enabled=false --set telemetry.tickMetrics=true --set telemetry.tick.interval=90s --set telemetry.network.interval=3m --set telemetry.disk.interval=15m --set telemetry.timeouts.exec=8s)
check_env "$rendered" RSDW_TELEMETRY_LIVENESS_INTERVAL 30s
check_env "$rendered" RSDW_TELEMETRY_METRICS_INTERVAL 2m
check_env "$rendered" RSDW_TELEMETRY_EXEC_METRICS_ENABLED false
check_env "$rendered" RSDW_TELEMETRY_PLAYERS_ENABLED false
check_env "$rendered" RSDW_TICK_METRICS true
check_env "$rendered" RSDW_TELEMETRY_TICK_INTERVAL 90s
check_env "$rendered" RSDW_TELEMETRY_NETWORK_INTERVAL 3m
check_env "$rendered" RSDW_TELEMETRY_DISK_INTERVAL 15m
check_env "$rendered" RSDW_TELEMETRY_EXEC_TIMEOUT 8s
for invalid in livenessInterval=15 livenessInterval=soon metricsInterval=-1m timeouts.kube=0s timeouts.health=bad execMetrics.enabled=maybe tickMetrics=maybe players.enabled=2 disk.interval= tick.interval=fast unknown=1; do
  if helm template telemetry-test "${chart[@]}" --set-string "telemetry.$invalid" >/dev/null 2>&1; then
    printf 'Unexpected chart success: %s\n' "$invalid" >&2
    exit 1
  fi
done
# Values the service rejects at startup (crash-looping pod after helm upgrade)
# must be rejected at render time, with the setting named in the message.
expect_rejected() {
  local message="$1" output
  shift
  if output=$(helm template telemetry-test "${chart[@]}" "$@" 2>&1); then
    printf 'Unexpected chart success: %s
' "$*" >&2
    exit 1
  fi
  if [[ "$output" != *"$message"* ]]; then
    printf 'Rejection of %s did not mention %s:
%s
' "$*" "$message" "$output" >&2
    exit 1
  fi
}
expect_rejected telemetry.livenessInterval --set telemetry.livenessInterval=1s
expect_rejected telemetry.livenessInterval --set telemetry.livenessInterval=6m
expect_rejected telemetry.metricsInterval --set telemetry.metricsInterval=5s
expect_rejected telemetry.metricsInterval --set telemetry.metricsInterval=24h
expect_rejected telemetry.timeouts.kube --set telemetry.timeouts.kube=500ms
expect_rejected telemetry.timeouts.health --set telemetry.timeouts.health=31s
expect_rejected telemetry.timeouts.exec --set telemetry.timeouts.exec=1m
expect_rejected telemetry.tick.interval --set telemetry.tick.interval=5s
expect_rejected telemetry.network.interval --set telemetry.network.interval=16m
expect_rejected telemetry.disk.interval --set telemetry.disk.interval=5s
expect_rejected telemetry.disk.interval --set telemetry.disk.interval=2h
expect_rejected telemetry.resources.enabled --set memoryPressure.enabled=true --set telemetry.resources.enabled=false
expect_rejected 'at most half of memoryPressure.duration' --set memoryPressure.enabled=true --set telemetry.metricsInterval=15m
expect_rejected 'at most half of memoryPressure.duration' --set memoryPressure.enabled=true --set memoryPressure.duration=1m --set telemetry.metricsInterval=45s
# The bounds are inclusive, and memory pressure is only constrained when enabled.
helm template telemetry-test "${chart[@]}" --set telemetry.livenessInterval=5s --set telemetry.metricsInterval=15m --set telemetry.disk.interval=1h --set telemetry.timeouts.kube=30s --set telemetry.tick.interval=10s >/dev/null
helm template telemetry-test "${chart[@]}" --set telemetry.metricsInterval=15m --set memoryPressure.duration=1m >/dev/null
helm template telemetry-test "${chart[@]}" --set memoryPressure.enabled=true --set memoryPressure.duration=30m --set telemetry.metricsInterval=15m >/dev/null
helm template telemetry-test "${chart[@]}" --set telemetry.resources.enabled=false >/dev/null
helm lint "${chart[@]}"
printf '%s\n' 'Telemetry chart checks passed'
