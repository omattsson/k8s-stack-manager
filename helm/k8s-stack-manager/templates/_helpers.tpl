{{/*
Chart name, truncated to 63 chars.
*/}}
{{- define "k8s-stack-manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name, truncated to 63 chars.
*/}}
{{- define "k8s-stack-manager.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "k8s-stack-manager.labels" -}}
helm.sh/chart: {{ include "k8s-stack-manager.name" . }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{/*
Backend labels.
*/}}
{{- define "k8s-stack-manager.backend.labels" -}}
{{ include "k8s-stack-manager.labels" . }}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-backend
app.kubernetes.io/component: backend
{{- end }}

{{/*
Backend selector labels.
*/}}
{{- define "k8s-stack-manager.backend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-backend
{{- end }}

{{/*
Frontend labels.
*/}}
{{- define "k8s-stack-manager.frontend.labels" -}}
{{ include "k8s-stack-manager.labels" . }}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-frontend
app.kubernetes.io/component: frontend
{{- end }}

{{/*
Frontend selector labels.
*/}}
{{- define "k8s-stack-manager.frontend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-frontend
{{- end }}

{{/*
Azurite labels.
*/}}
{{- define "k8s-stack-manager.azurite.labels" -}}
{{ include "k8s-stack-manager.labels" . }}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-azurite
app.kubernetes.io/component: azurite
{{- end }}

{{/*
Azurite selector labels.
*/}}
{{- define "k8s-stack-manager.azurite.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-azurite
{{- end }}

{{/*
Backend service names.
*/}}
{{- define "k8s-stack-manager.backend.stableService" -}}
{{ include "k8s-stack-manager.fullname" . }}-backend-stable
{{- end }}

{{- define "k8s-stack-manager.backend.canaryService" -}}
{{ include "k8s-stack-manager.fullname" . }}-backend-canary
{{- end }}

{{/*
Frontend service names.
*/}}
{{- define "k8s-stack-manager.frontend.stableService" -}}
{{ include "k8s-stack-manager.fullname" . }}-frontend-stable
{{- end }}

{{- define "k8s-stack-manager.frontend.canaryService" -}}
{{ include "k8s-stack-manager.fullname" . }}-frontend-canary
{{- end }}

{{/*
Azurite service name.
*/}}
{{- define "k8s-stack-manager.azurite.serviceName" -}}
{{ include "k8s-stack-manager.fullname" . }}-azurite
{{- end }}

{{/*
Backend image.
*/}}
{{- define "k8s-stack-manager.backend.image" -}}
{{- if .Values.global.imageRegistry }}
{{- printf "%s/%s:%s" .Values.global.imageRegistry .Values.backend.image.repository (.Values.backend.image.tag | default .Chart.AppVersion) }}
{{- else }}
{{- printf "%s:%s" .Values.backend.image.repository (.Values.backend.image.tag | default .Chart.AppVersion) }}
{{- end }}
{{- end }}

{{/*
Frontend image.
*/}}
{{- define "k8s-stack-manager.frontend.image" -}}
{{- if .Values.global.imageRegistry }}
{{- printf "%s/%s:%s" .Values.global.imageRegistry .Values.frontend.image.repository (.Values.frontend.image.tag | default .Chart.AppVersion) }}
{{- else }}
{{- printf "%s:%s" .Values.frontend.image.repository (.Values.frontend.image.tag | default .Chart.AppVersion) }}
{{- end }}
{{- end }}
