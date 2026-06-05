{{/*
Expand the name of the chart.
*/}}
{{- define "rezuscloud.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name — release-name + chart-name, truncated.
*/}}
{{- define "rezuscloud.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart name and version label.
*/}}
{{- define "rezuscloud.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "rezuscloud.labels" -}}
helm.sh/chart: {{ include "rezuscloud.chart" . }}
{{ include "rezuscloud.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: management-plane
{{- end -}}

{{/*
Selector labels — must match pod template.
*/}}
{{- define "rezuscloud.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rezuscloud.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "rezuscloud.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "rezuscloud.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image reference: repo:tag (or repo:appVersion when tag is empty).
*/}}
{{- define "rezuscloud.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Resolve the secret name to use for JWT/admin/join credentials.
Priority: existingSecret > generated fullname-based name.
*/}}
{{- define "rezuscloud.secretName" -}}
{{- if .Values.existingSecret -}}
{{- .Values.existingSecret -}}
{{- else -}}
{{- include "rezuscloud.fullname" . -}}
{{- end -}}
{{- end -}}

{{/*
Validate required configuration. Fails fast with a clear message if jwtSecret
is missing and no existingSecret is provided.
*/}}
{{- define "rezuscloud.validate" -}}
{{- if not .Values.jwtSecret -}}
{{- if not .Values.existingSecret -}}
{{- fail "rezuscloud.jwtSecret is required (or set existingSecret). Generate with: openssl rand -hex 32" -}}
{{- end -}}
{{- end -}}
{{- end -}}
