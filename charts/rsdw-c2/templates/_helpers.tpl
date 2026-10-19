{{- define "rsdwc2.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "rsdwc2.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "rsdwc2.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- define "rsdwc2.labels" -}}
app.kubernetes.io/name: {{ include "rsdwc2.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: Helm
{{- end -}}
{{- define "rsdwc2.image" -}}
{{- if .Values.image.tag -}}{{ .Values.image.repository }}:{{ .Values.image.tag }}{{- else -}}{{ .Values.image.repository }}:{{ .Chart.AppVersion }}{{- end -}}
{{- end -}}
{{/*
Duration values are length-checked by values.schema.json. The bounds below
mirror telemetry_config.go and memory_pressure.go: the service refuses to start
(a crash-looping pod after helm upgrade) when a value is out of range, so the
chart refuses to render instead. A Go duration is converted to nanoseconds by
adding it to a fixed date.
*/}}
{{- define "rsdwc2.nanos" -}}
{{- $base := mustToDate "2006-01-02" "2000-01-01" -}}
{{- printf "%d" (($base | mustDateModify .).Sub $base) -}}
{{- end -}}
{{- define "rsdwc2.checkDuration" -}}
{{- if .value -}}
{{- $nanos := include "rsdwc2.nanos" .value | int64 -}}
{{- if or (lt $nanos (include "rsdwc2.nanos" .min | int64)) (gt $nanos (include "rsdwc2.nanos" .max | int64)) -}}
{{- fail (printf "%s must be between %s and %s, got %q" .name .min .max .value) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- define "rsdwc2.validateTelemetry" -}}
{{- $t := .Values.telemetry -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.livenessInterval" "value" $t.livenessInterval "min" "5s" "max" "5m") -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.metricsInterval" "value" $t.metricsInterval "min" "10s" "max" "15m") -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.timeouts.kube" "value" $t.timeouts.kube "min" "1s" "max" "30s") -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.timeouts.health" "value" $t.timeouts.health "min" "1s" "max" "30s") -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.timeouts.exec" "value" $t.timeouts.exec "min" "1s" "max" "30s") -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.tick.interval" "value" $t.tick.interval "min" "10s" "max" "15m") -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.network.interval" "value" $t.network.interval "min" "10s" "max" "15m") -}}
{{- include "rsdwc2.checkDuration" (dict "name" "telemetry.disk.interval" "value" $t.disk.interval "min" "1m" "max" "1h") -}}
{{- if .Values.memoryPressure.enabled -}}
{{- if not $t.resources.enabled -}}
{{- fail "memoryPressure.enabled requires telemetry.resources.enabled, which supplies the memory samples" -}}
{{- end -}}
{{- if gt (mul 2 (include "rsdwc2.nanos" $t.metricsInterval | int64)) (include "rsdwc2.nanos" .Values.memoryPressure.duration | int64) -}}
{{- fail (printf "telemetry.metricsInterval (%s) must be at most half of memoryPressure.duration (%s) when memoryPressure.enabled is true: lower the metrics interval or raise the duration" $t.metricsInterval .Values.memoryPressure.duration) -}}
{{- end -}}
{{- end -}}
{{- end -}}
