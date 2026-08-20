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
{{/*
  The terminal is NOT validated here any more, and that is a decision rather
  than an omission.

  It used to fail the render on either sandbox: the shell needed a sandbox to
  contain it, and the Kubernetes backend runs a gear Job to completion rather
  than attaching to one. Neither is the shape of it now — the terminal opens a
  shell on the machine the server runs on, which in a cluster is this pod, as
  the account this container runs as. That works here, and refusing it would be
  this chart deciding something the operator is entitled to decide.

  What it means is said in values.yaml beside the setting, and the default is
  still false, because on a cluster the person who opens the interface is not
  necessarily the person who applied this chart.
*/}}
{{- if and .Values.config.egress (eq .Values.config.sandbox "subprocess") -}}
{{- fail "config.egress needs a sandbox. An unsandboxed gear runs with the server's file access and can rewrite the configuration and the grants table, so the gate would be decorative. Use sandbox: kubernetes." -}}
{{- end -}}
{{- if and (eq .Values.config.sandbox "kubernetes") (not .Values.serviceAccount.automountServiceAccountToken) -}}
{{- fail "sandbox: kubernetes needs serviceAccount.automountServiceAccountToken: true — the server creates a Job per gear run and that is what it authenticates with. The gear's own pod still mounts no token." -}}
{{- end -}}
{{- if not (has .Values.config.sandbox (list "kubernetes" "subprocess")) -}}
{{- fail "config.sandbox must be kubernetes or subprocess in this chart. There is no Docker daemon inside a pod." -}}
{{- end -}}
{{- if and (not .Values.persistence.enabled) (not .Values.persistence.existingClaim) -}}
{{- fail "persistence.enabled is false: the SQLite database would live in the pod's filesystem and be destroyed on every restart. Set persistence.enabled or persistence.existingClaim." -}}
{{- end -}}
{{- if and .Values.auth.adminToken (lt (len .Values.auth.adminToken) 24) -}}
{{- fail "auth.adminToken must be at least 24 characters: it seeds the admin's credential, and the server refuses a shorter one at startup." -}}
{{- end -}}
{{- if and .Values.auth.adminPassword (lt (len .Values.auth.adminPassword) 8) -}}
{{- fail "auth.adminPassword must be at least 8 characters: it is what the admin signs in with, and the server refuses a shorter one at startup. Leave it empty and the chart generates one." -}}
{{- end -}}
{{- if gt (len .Values.auth.adminPassword) 72 -}}
{{- fail "auth.adminPassword is over 72 bytes. That is bcrypt's limit, not a policy: it refuses rather than truncates, so the server would fail to start on every start. Use a shorter one." -}}
{{- end -}}
{{- end -}}
