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
for source in RESOURCES PLAYERS TICK NETWORK DISK; do
  check_env "$rendered" "RSDW_TELEMETRY_${source}_ENABLED" true
done
check_env "$rendered" RSDW_TELEMETRY_DISK_INTERVAL 5m
absent_env "$rendered" RSDW_TELEMETRY_TICK_INTERVAL
absent_env "$rendered" RSDW_TELEMETRY_NETWORK_INTERVAL
rendered=$(helm template telemetry-test "${chart[@]}" --set telemetry.livenessInterval=30s --set telemetry.metricsInterval=2m --set telemetry.execMetrics.enabled=false --set telemetry.players.enabled=false --set telemetry.tick.interval=90s --set telemetry.network.interval=3m --set telemetry.disk.interval=15m --set telemetry.timeouts.exec=8s)
check_env "$rendered" RSDW_TELEMETRY_LIVENESS_INTERVAL 30s
check_env "$rendered" RSDW_TELEMETRY_METRICS_INTERVAL 2m
check_env "$rendered" RSDW_TELEMETRY_EXEC_METRICS_ENABLED false
check_env "$rendered" RSDW_TELEMETRY_PLAYERS_ENABLED false
check_env "$rendered" RSDW_TELEMETRY_TICK_INTERVAL 90s
check_env "$rendered" RSDW_TELEMETRY_NETWORK_INTERVAL 3m
check_env "$rendered" RSDW_TELEMETRY_DISK_INTERVAL 15m
check_env "$rendered" RSDW_TELEMETRY_EXEC_TIMEOUT 8s
for invalid in livenessInterval=15 livenessInterval=soon metricsInterval=-1m timeouts.kube=0s timeouts.health=bad execMetrics.enabled=maybe players.enabled=2 disk.interval= tick.interval=fast unknown=1; do
  if helm template telemetry-test "${chart[@]}" --set-string "telemetry.$invalid" >/dev/null 2>&1; then
    printf 'Unexpected chart success: %s\n' "$invalid" >&2
    exit 1
  fi
done
helm lint "${chart[@]}"
printf '%s\n' 'Telemetry chart checks passed'
