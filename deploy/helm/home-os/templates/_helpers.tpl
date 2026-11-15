{{- define "home-os.labels" -}}
app.kubernetes.io/name: home-os
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "home-os.image" -}}
{{ .Values.global.imageRegistry }}/{{ .image }}:{{ .Values.global.imageTag }}
{{- end -}}

{{- define "home-os.databaseUrl" -}}
{{- if eq .Values.database.mode "managed" -}}
postgres://app:{{ .Values.secrets.dbPassword }}@home-os-postgres-rw.home-os:5432/app?sslmode=disable
{{- else -}}
postgres://{{ .Values.database.external.user }}:{{ .Values.database.external.password }}@{{ .Values.database.external.host }}:{{ .Values.database.external.port }}/{{ .Values.database.external.database }}?sslmode=disable
{{- end -}}
{{- end -}}

{{/*
Public origin users and OAuth clients reach Home OS at (scheme://host[:port],
no trailing slash). Set global.publicUrl for a real hostname; empty falls back
to the local docker-desktop Envoy origin http://localhost:<envoy.port>.
*/}}
{{- define "home-os.publicUrl" -}}
{{- if .Values.global.publicUrl -}}
{{- trimSuffix "/" .Values.global.publicUrl -}}
{{- else -}}
http://localhost:{{ .Values.envoy.port }}
{{- end -}}
{{- end -}}

{{/* Dex OIDC issuer: <publicUrl>/dex. Must match the iss claim API and MCP verify. */}}
{{- define "home-os.dexIssuer" -}}
{{ include "home-os.publicUrl" . }}/dex
{{- end -}}

{{/* home-os-ui OAuth redirect URI: dex.uiRedirectURI override, else <publicUrl>/api/auth/callback. */}}
{{- define "home-os.uiRedirectURI" -}}
{{- if .Values.dex.uiRedirectURI -}}
{{ .Values.dex.uiRedirectURI }}
{{- else -}}
{{ include "home-os.publicUrl" . }}/api/auth/callback
{{- end -}}
{{- end -}}

{{/* API PUBLIC_URL (links in emails etc.): api.publicUrl override, else publicUrl. */}}
{{- define "home-os.apiPublicUrl" -}}
{{- if .Values.api.publicUrl -}}
{{ .Values.api.publicUrl }}
{{- else -}}
{{ include "home-os.publicUrl" . }}
{{- end -}}
{{- end -}}
