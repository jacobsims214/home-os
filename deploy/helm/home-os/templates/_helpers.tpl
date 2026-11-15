{{- define "home-os.labels" -}}
app.kubernetes.io/name: home-os
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "home-os.image" -}}
{{ .Values.global.imageRegistry }}/{{ .image }}:{{ .Values.global.imageTag }}
{{- end -}}

{{/*
Secrets. Every credential reaches pods through secretKeyRef; nothing secret is
rendered into env `value:` fields. Each group has an existingSecret override;
when it is set the chart does not render its own Secret for that group.
*/}}

{{/* App secrets (jwtSecret, calendarSecret, typesenseApiKey, encryptionKey, adminEmail, adminPassword[, smtpPassword]). */}}
{{- define "home-os.secretsName" -}}
{{- .Values.secrets.existingSecret | default "home-os-secrets" -}}
{{- end -}}

{{/* Secret holding the database password (managed: CNPG basic-auth for the `app` user). */}}
{{- define "home-os.dbSecretName" -}}
{{- if eq .Values.database.mode "managed" -}}
{{- .Values.database.managed.existingSecret | default "home-os-db-secret" -}}
{{- else -}}
{{- .Values.database.external.existingSecret | default "home-os-db-secret" -}}
{{- end -}}
{{- end -}}

{{/* Key of the password in home-os.dbSecretName. Managed mode is always "password" (CNPG basic-auth). */}}
{{- define "home-os.dbSecretPasswordKey" -}}
{{- if eq .Values.database.mode "managed" -}}
password
{{- else -}}
{{- .Values.database.external.existingSecretPasswordKey | default "password" -}}
{{- end -}}
{{- end -}}

{{/* True when the chart must render home-os-db-secret itself. */}}
{{- define "home-os.renderDbSecret" -}}
{{- if eq .Values.database.mode "managed" -}}
{{- if not .Values.database.managed.existingSecret -}}true{{- end -}}
{{- else -}}
{{- if not .Values.database.external.existingSecret -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{/* SMTP password Secret: smtp.existingSecret, else the app secret (key smtpPassword). */}}
{{- define "home-os.smtpSecretName" -}}
{{- .Values.smtp.existingSecret | default (include "home-os.secretsName" .) -}}
{{- end -}}

{{- define "home-os.smtpSecretPasswordKey" -}}
{{- if .Values.smtp.existingSecret -}}
{{- .Values.smtp.existingSecretPasswordKey | default "password" -}}
{{- else -}}
smtpPassword
{{- end -}}
{{- end -}}

{{/*
DATABASE_URL with the password referenced as $(DB_PASSWORD), which Kubernetes
expands from the preceding DB_PASSWORD env var (secretKeyRef). The password
must be URL-safe (no @ : / ? # % etc.); otherwise percent-encode it in the
Secret.
*/}}
{{- define "home-os.databaseUrl" -}}
{{- if eq .Values.database.mode "managed" -}}
postgres://app:$(DB_PASSWORD)@home-os-postgres-rw.{{ .Values.namespace }}:5432/app?sslmode=disable
{{- else -}}
postgres://{{ .Values.database.external.user }}:$(DB_PASSWORD)@{{ .Values.database.external.host }}:{{ .Values.database.external.port }}/{{ .Values.database.external.database }}?sslmode={{ .Values.database.external.sslmode | default "disable" }}
{{- end -}}
{{- end -}}

{{/* Container env entries DB_PASSWORD + DATABASE_URL. Use: {{- include "home-os.databaseEnv" . | nindent 8 }} */}}
{{- define "home-os.databaseEnv" -}}
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "home-os.dbSecretName" . }}
      key: {{ include "home-os.dbSecretPasswordKey" . }}
- name: DATABASE_URL
  value: {{ include "home-os.databaseUrl" . | quote }}
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

{{/* In-cluster API base URL (Service home-os-api, port api.port), used by the UI server side. */}}
{{- define "home-os.apiInternalUrl" -}}
http://home-os-api:{{ .Values.api.port }}
{{- end -}}
