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
postgres://app:wCGVeROLHi1br16yJ5QjRloXH815VrFEbkinBnw1CSygf3DJEtKsGJJjFYFlZsy4@home-os-postgres-rw.home-os:5432/app?sslmode=disable
{{- else -}}
postgres://{{ .Values.database.external.user }}:{{ .Values.database.external.password }}@{{ .Values.database.external.host }}:{{ .Values.database.external.port }}/{{ .Values.database.external.database }}?sslmode=disable
{{- end -}}
{{- end -}}
