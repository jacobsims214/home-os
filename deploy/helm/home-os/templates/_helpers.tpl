{{- define "home-os.labels" -}}
app.kubernetes.io/name: home-os
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Tag for every home-os app image (api, ui, mcp, worker, calendar, migrations).
All six must run the same build, so there is one tag: global.imageTag.

Guard (#4679): release.yml only moves `latest` on a release or a run on main,
so on any other branch `latest` silently resolves to an old build (and an old
migrations image). The render fails when global.imageTag is
  - the Argo manifest's placeholder (sha-SET_IN_4357), always; or
  - empty or `latest`, unless global.allowLatestImageTag=true
    (local docker-desktop only: local-dev-values.yaml sets it).
Set it to the immutable sha-<short> tag of one release.yml run.
*/}}
{{- define "home-os.imageTag" -}}
{{- $tag := toString (.Values.global.imageTag | default "") -}}
{{- if contains "SET_IN_4357" $tag -}}
{{- fail (printf "global.imageTag is still the placeholder %q. Dispatch release.yml on the deploy branch and set global.imageTag to that run's sha-<short> tag (#4357) before syncing." $tag) -}}
{{- end -}}
{{- if and (or (not $tag) (eq $tag "latest")) (not .Values.global.allowLatestImageTag) -}}
{{- fail (printf "global.imageTag is %q: `latest` only moves on releases/main and would deploy an old build and old migrations. Set global.imageTag to an immutable sha-<short> tag from release.yml, or set global.allowLatestImageTag=true for local docker-desktop only." (default "<empty>" $tag)) -}}
{{- end -}}
{{- $tag -}}
{{- end -}}

{{- define "home-os.image" -}}
{{ .Values.global.imageRegistry }}/{{ .image }}:{{ include "home-os.imageTag" . }}
{{- end -}}

{{/*
Secrets. Every credential reaches pods through secretKeyRef; nothing secret is
rendered into env `value:` fields. Each group has an existingSecret override;
when it is set the chart does not render its own Secret for that group.
*/}}

{{/* App secrets (calendarSecret, typesenseApiKey, encryptionKey, adminEmail, adminPassword[, smtpPassword]). */}}
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

{{/* Agent-credentials master key (#4446): agentCredentials.existingSecret, else the app secret. */}}
{{- define "home-os.agentCredentialsSecretName" -}}
{{- .Values.agentCredentials.existingSecret | default (include "home-os.secretsName" .) -}}
{{- end -}}

{{- define "home-os.agentCredentialsSecretKey" -}}
{{- if .Values.agentCredentials.existingSecret -}}
{{- .Values.agentCredentials.existingSecretKey | default "agentCredentialsKey" -}}
{{- else -}}
agentCredentialsKey
{{- end -}}
{{- end -}}

{{/* AGENT_CREDENTIALS_KEY env for the api and mcp. Optional: without it sharing is off. */}}
{{- define "home-os.agentCredentialsEnv" -}}
- name: AGENT_CREDENTIALS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "home-os.agentCredentialsSecretName" . }}
      key: {{ include "home-os.agentCredentialsSecretKey" . }}
      optional: true
{{- end -}}

{{/*
Database connection parts, shared by home-os.databaseUrl and Dex's storage
config. Managed: the chart's CNPG cluster as `app`, no TLS. External:
database.external.*. home-os.dbHost is the bare Service name in managed mode;
DATABASE_URL qualifies it with the namespace itself.
*/}}
{{- define "home-os.dbHost" -}}
{{- if eq .Values.database.mode "managed" -}}
home-os-postgres-rw
{{- else -}}
{{- .Values.database.external.host -}}
{{- end -}}
{{- end -}}

{{- define "home-os.dbPort" -}}
{{- if eq .Values.database.mode "managed" -}}
5432
{{- else -}}
{{- .Values.database.external.port -}}
{{- end -}}
{{- end -}}

{{- define "home-os.dbUser" -}}
{{- if eq .Values.database.mode "managed" -}}
app
{{- else -}}
{{- .Values.database.external.user -}}
{{- end -}}
{{- end -}}

{{- define "home-os.dbSslMode" -}}
{{- if eq .Values.database.mode "managed" -}}
disable
{{- else -}}
{{- .Values.database.external.sslmode | default "disable" -}}
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
postgres://{{ include "home-os.dbUser" . }}:$(DB_PASSWORD)@{{ include "home-os.dbHost" . }}.{{ .Values.namespace }}:{{ include "home-os.dbPort" . }}/app?sslmode={{ include "home-os.dbSslMode" . }}
{{- else -}}
postgres://{{ include "home-os.dbUser" . }}:$(DB_PASSWORD)@{{ include "home-os.dbHost" . }}:{{ include "home-os.dbPort" . }}/{{ .Values.database.external.database }}?sslmode={{ include "home-os.dbSslMode" . }}
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
no trailing slash). global.publicUrl is the single source for the Dex issuer,
the home-os-ui redirect URI, the UI/API/MCP public URLs, the MCP resource URL
and the default ingress host.

Guard (#4675): Dex is always deployed, and a localhost issuer breaks login for
every real browser, so the render fails when publicUrl is
  - the Argo manifest's placeholder (HOMEOS_PRIVATE_HOSTNAME...), always; or
  - empty, localhost, 127.x or [::1], unless global.allowLocalPublicUrl=true
    (local docker-desktop only: local-dev-values.yaml sets it).
Empty + allowLocalPublicUrl = http://localhost:<envoy.port>.
*/}}
{{- define "home-os.publicUrl" -}}
{{- $url := trimSuffix "/" (.Values.global.publicUrl | default "") -}}
{{- if contains "HOMEOS_PRIVATE_HOSTNAME" $url -}}
{{- fail (printf "global.publicUrl is still the placeholder %q. Set it to the private Home OS hostname (chosen in #4389, applied by #4357) before syncing." $url) -}}
{{- end -}}
{{- $local := or (not $url) (regexMatch "^https?://(localhost|127\\.[0-9.]+|\\[::1\\])(:[0-9]+)?(/.*)?$" $url) -}}
{{- if and $local (not .Values.global.allowLocalPublicUrl) -}}
{{- fail (printf "global.publicUrl is %q: Dex would issue tokens for a localhost issuer and login would break. Set global.publicUrl to the real https origin, or set global.allowLocalPublicUrl=true for local docker-desktop only." (default "<empty>" $url)) -}}
{{- end -}}
{{- if $url -}}
{{- $url -}}
{{- else -}}
http://localhost:{{ .Values.envoy.port }}
{{- end -}}
{{- end -}}

{{/* Ingress host: ingress.host override, else the hostname of global.publicUrl. */}}
{{- define "home-os.ingressHost" -}}
{{- if .Values.ingress.host -}}
{{- .Values.ingress.host -}}
{{- else -}}
{{- (urlParse (include "home-os.publicUrl" .)).hostname -}}
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

{{/*
PodSecurity "restricted" (#4421). Every workload runs as a fixed non-root uid
matching its image's own runtime user, with no privilege escalation, all
capabilities dropped, the RuntimeDefault seccomp profile and a read-only root
filesystem; writable paths are emptyDir mounts (home-os.tmpVolumeMount).

Pod level. Use: {{- include "home-os.podSecurityContext" (dict "uid" 65532) | nindent 6 }}
Optional "gid" (defaults to uid); fsGroup = gid so mounted volumes are writable.
*/}}
{{- define "home-os.podSecurityContext" -}}
{{- $gid := .gid | default .uid -}}
securityContext:
  runAsNonRoot: true
  runAsUser: {{ .uid }}
  runAsGroup: {{ $gid }}
  fsGroup: {{ $gid }}
  seccompProfile:
    type: RuntimeDefault
{{- end -}}

{{/* Container level. Use: {{- include "home-os.containerSecurityContext" . | nindent 8 }} */}}
{{- define "home-os.containerSecurityContext" -}}
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: ["ALL"]
{{- end -}}

{{/* emptyDir /tmp for read-only root filesystems: mount + volume. */}}
{{- define "home-os.tmpVolumeMount" -}}
- name: tmp
  mountPath: /tmp
{{- end -}}

{{- define "home-os.tmpVolume" -}}
- name: tmp
  emptyDir: {}
{{- end -}}
