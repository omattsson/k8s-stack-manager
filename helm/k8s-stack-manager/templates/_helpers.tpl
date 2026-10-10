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
Name of the Lease for the backend leader election: backend.env
LEADER_ELECTION_LEASE_NAME, else backend.leaderElection.leaseName, else
"<fullname>-workers".
*/}}
{{- define "k8s-stack-manager.leaderElection.leaseName" -}}
{{- if hasKey .Values.backend.env "LEADER_ELECTION_LEASE_NAME" }}
{{- index .Values.backend.env "LEADER_ELECTION_LEASE_NAME" }}
{{- else }}
{{- default (printf "%s-workers" (include "k8s-stack-manager.fullname" .)) .Values.backend.leaderElection.leaseName | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Backend container env: the pod name and namespace (downward API; the leader
election identity and Lease namespace), then backend.extraEnv.
*/}}
{{- define "k8s-stack-manager.backend.env" -}}
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
{{- with .Values.backend.extraEnv }}
{{ toYaml . }}
{{- end }}
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
MySQL labels.
*/}}
{{- define "k8s-stack-manager.mysql.labels" -}}
{{ include "k8s-stack-manager.labels" . }}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-mysql
app.kubernetes.io/component: mysql
{{- end }}

{{/*
MySQL selector labels.
*/}}
{{- define "k8s-stack-manager.mysql.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-mysql
{{- end }}

{{/*
MySQL service name.
*/}}
{{- define "k8s-stack-manager.mysql.serviceName" -}}
{{ include "k8s-stack-manager.fullname" . }}-mysql
{{- end }}

{{/*
OTel Collector labels.
*/}}
{{- define "k8s-stack-manager.otel.labels" -}}
{{ include "k8s-stack-manager.labels" . }}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-otel-collector
app.kubernetes.io/component: otel-collector
{{- end }}

{{/*
OTel Collector selector labels.
*/}}
{{- define "k8s-stack-manager.otel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s-stack-manager.fullname" . }}-otel-collector
{{- end }}

{{/*
OTel Collector service name.
*/}}
{{- define "k8s-stack-manager.otel.serviceName" -}}
{{ include "k8s-stack-manager.fullname" . }}-otel-collector
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

{{/*
Backend service name — stable service when rollouts enabled, simple service otherwise.
*/}}
{{- define "k8s-stack-manager.backend.serviceName" -}}
{{- if .Values.argoRollouts.enabled }}
{{- include "k8s-stack-manager.backend.stableService" . }}
{{- else }}
{{- printf "%s-backend" (include "k8s-stack-manager.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Frontend service name — stable service when rollouts enabled, simple service otherwise.
*/}}
{{- define "k8s-stack-manager.frontend.serviceName" -}}
{{- if .Values.argoRollouts.enabled }}
{{- include "k8s-stack-manager.frontend.stableService" . }}
{{- else }}
{{- printf "%s-frontend" (include "k8s-stack-manager.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Validate ingress type.
*/}}
{{- define "k8s-stack-manager.validateIngressType" -}}
{{- $valid := list "traefik" "ingress" "none" -}}
{{- if not (has .Values.ingress.type $valid) -}}
{{- fail (printf "ingress.type must be one of: traefik, ingress, none (got: %s)" .Values.ingress.type) -}}
{{- end -}}
{{- end -}}

{{/*
Return "true" when the chart ingress terminates TLS:
- ingress.type=traefik with ingress.traefik.tls.secretName set, or
- ingress.type=ingress with a non-empty ingress.tls list.
TLS terminated outside the chart (for example at a load balancer) is not
detected; set backend.env.SECURE_COOKIES explicitly in that case.
*/}}
{{- define "k8s-stack-manager.ingressTLSEnabled" -}}
{{- if and (eq .Values.ingress.type "traefik") .Values.ingress.traefik.tls .Values.ingress.traefik.tls.secretName -}}
true
{{- else if and (eq .Values.ingress.type "ingress") .Values.ingress.tls -}}
true
{{- end -}}
{{- end -}}

{{/*
Return "true" when branding.files or branding.binaryFiles has a file: the
chart then renders the frontend branding ConfigMap and mounts it into nginx
at /usr/share/nginx/html/branding/.
*/}}
{{- define "k8s-stack-manager.brandingFilesEnabled" -}}
{{- with .Values.branding -}}
{{- if or .files .binaryFiles -}}
true
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Validate branding.files and branding.binaryFiles: each file name must match
^[-._a-zA-Z0-9]+$ (a ConfigMap key, no path), and the total size of the
files must stay at or below 900 KiB (a ConfigMap holds at most 1 MiB).
binaryFiles count with their decoded size.
*/}}
{{- define "k8s-stack-manager.validateBrandingFiles" -}}
{{- $total := 0 -}}
{{- range $name, $content := (.Values.branding.files | default dict) -}}
{{- if not (regexMatch "^[-._a-zA-Z0-9]+$" $name) -}}
{{- fail (printf "branding.files: invalid file name %q (allowed: letters, digits, '-', '_', '.')" $name) -}}
{{- end -}}
{{- $total = add $total (len $content) -}}
{{- end -}}
{{- range $name, $content := (.Values.branding.binaryFiles | default dict) -}}
{{- if not (regexMatch "^[-._a-zA-Z0-9]+$" $name) -}}
{{- fail (printf "branding.binaryFiles: invalid file name %q (allowed: letters, digits, '-', '_', '.')" $name) -}}
{{- end -}}
{{- $total = add $total (len (b64dec $content)) -}}
{{- end -}}
{{- if gt (int $total) 921600 -}}
{{- fail (printf "branding.files and branding.binaryFiles total %d bytes; the limit is 900 KiB (921600 bytes)" (int $total)) -}}
{{- end -}}
{{- end -}}
