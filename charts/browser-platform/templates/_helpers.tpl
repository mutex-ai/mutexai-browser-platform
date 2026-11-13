{{- define "browser-platform.name" -}}
{{- printf "%s" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "browser-platform.selectorLabels" -}}
app.kubernetes.io/name: browser-platform
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "browser-platform.labels" -}}
{{ include "browser-platform.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "browser-platform.image" -}}
{{- $repository := .Values.image.repository -}}
{{- if .Values.image.registry -}}
{{- $repository = printf "%s/%s" .Values.image.registry $repository -}}
{{- end -}}
{{- printf "%s:%s" $repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}
