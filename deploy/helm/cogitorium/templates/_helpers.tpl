{{- define "cogitorium.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "cogitorium.fullname" -}}
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

{{- define "cogitorium.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "cogitorium.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "cogitorium.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cogitorium.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "cogitorium.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "cogitorium.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "cogitorium.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/*
Refuse the configurations that cannot work, at template time.

A chart that renders something broken and lets the cluster discover it is a
chart that turns a five-second failure into a debugging session. Both of these
are refusals the server itself also makes; failing here means failing before
anything is applied.
*/}}
{{- define "cogitorium.validate" -}}
{{- if .Values.config.terminal -}}
{{- fail "config.terminal cannot be enabled in-cluster: the shell is interactive code execution and there is no sandbox to contain it here. The server refuses it too — this is the earlier of the two refusals." -}}
{{- end -}}
{{- if and .Values.config.egress (ne .Values.config.sandbox "docker") -}}
{{- fail "config.egress needs a sandbox. An unsandboxed gear runs with the server's file access and can rewrite the configuration and the grants table, so the gate would be decorative. Gear execution as Kubernetes Jobs is the fix and is not built yet." -}}
{{- end -}}
{{- if and (not .Values.persistence.enabled) (not .Values.persistence.existingClaim) -}}
{{- fail "persistence.enabled is false: the SQLite database would live in the pod's filesystem and be destroyed on every restart. Set persistence.enabled or persistence.existingClaim." -}}
{{- end -}}
{{- if and .Values.auth.adminToken (lt (len .Values.auth.adminToken) 24) -}}
{{- fail "auth.adminToken must be at least 24 characters: it seeds the admin's credential, and the server refuses a shorter one at startup." -}}
{{- end -}}
{{- end -}}
