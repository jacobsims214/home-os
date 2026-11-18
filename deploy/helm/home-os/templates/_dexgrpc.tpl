{{/*
Dex gRPC mTLS (#697).

The Dex gRPC admin listener has no application-layer authentication, so every
connection to it is mTLS: Dex requires a client certificate signed by the
chart's private CA (grpc.tlsClientCA), and the api and mcp clients present one
and verify Dex against the same CA. These helpers keep the paths in one place
so the ConfigMap, the Dex Deployment and both client Deployments cannot drift
apart.
*/}}

{{/* Directory the TLS material is mounted at, in Dex and in its clients. */}}
{{- define "home-os.dexGrpcTLSDir" -}}
/etc/dex-grpc-tls
{{- end -}}

{{/*
Volume mounting tls.crt/tls.key from a leaf Secret plus ca.crt from the CA
Secret as one projected volume.

ca.crt is taken from the CA Secret rather than from the leaf Secret's own
ca.crt: cert-manager does populate ca.crt on leaves it issues, but that is a
property of the issuer, and a missing key in a projected volume is a hard mount
failure — the pod would not start, with no hint that the cause is the issuer
rather than the chart. Sourcing it from the CA Secret makes the mount depend
only on the two Secrets this chart owns.
*/}}
{{- define "home-os.dexGrpcTLSVolume" -}}
- name: dex-grpc-tls
  projected:
    sources:
    - secret:
        name: {{ .leafSecret }}
        items:
        - key: tls.crt
          path: tls.crt
        - key: tls.key
          path: tls.key
    - secret:
        name: {{ .Values.dex.grpcTLS.caSecretName }}
        items:
        - key: tls.crt
          path: ca.crt
{{- end -}}

{{/* Volume for the Dex pod: server leaf + CA. */}}
{{- define "home-os.dexGrpcServerVolume" -}}
{{- include "home-os.dexGrpcTLSVolume" (dict "Values" .Values "leafSecret" .Values.dex.grpcTLS.serverSecretName) -}}
{{- end -}}

{{/* Volume for the api and mcp pods: client leaf + CA. */}}
{{- define "home-os.dexGrpcClientVolume" -}}
{{- include "home-os.dexGrpcTLSVolume" (dict "Values" .Values "leafSecret" .Values.dex.grpcTLS.clientSecretName) -}}
{{- end -}}

{{- define "home-os.dexGrpcClientVolumeMount" -}}
- name: dex-grpc-tls
  mountPath: {{ include "home-os.dexGrpcTLSDir" . }}
  readOnly: true
{{- end -}}

{{/*
Client env for api and mcp. DEX_GRPC_INSECURE is deliberately never rendered:
outside local docker compose the only way to reach Dex is over mTLS. With
grpcTLS.enabled=false the Secret does not exist, so the pods must say
DEX_GRPC_INSECURE=true themselves (dex.grpcTLS.enabled=false is the documented
opt-out; the NetworkPolicy in dex/networkpolicy.yaml is then the only control).
*/}}
{{- define "home-os.dexGrpcClientEnv" -}}
{{- if .Values.dex.grpcTLS.enabled }}
- name: DEX_GRPC_TLS_CERT
  value: "{{ include "home-os.dexGrpcTLSDir" . }}/tls.crt"
- name: DEX_GRPC_TLS_KEY
  value: "{{ include "home-os.dexGrpcTLSDir" . }}/tls.key"
- name: DEX_GRPC_TLS_CA
  value: "{{ include "home-os.dexGrpcTLSDir" . }}/ca.crt"
- name: DEX_GRPC_TLS_SERVER_NAME
  value: "home-os-dex.{{ .Values.namespace }}.svc"
{{- else }}
- name: DEX_GRPC_INSECURE
  value: "true"
{{- end }}
{{- end -}}

{{/*
Guard: the opt-out must not also disable its own compensating control. With
grpcTLS.enabled=false the gRPC listener is plaintext, so the NetworkPolicy is
the only thing between the network and CreatePassword; requiring both to be
off would leave the admin API open to every pod in the cluster.
*/}}
{{- define "home-os.dexGrpcAssertCovered" -}}
{{- if not .Values.dex.grpcTLS.enabled -}}
{{- if not .Values.dex.networkPolicy.enabled -}}
{{- fail "dex.grpcTLS.enabled=false leaves the Dex gRPC admin API (port 5557) plaintext, and dex.networkPolicy.enabled=false removes the only control left on it. Enable one: mTLS (#697) or the NetworkPolicy that limits 5557 to dex.networkPolicy.grpcClientApps (#4682)." -}}
{{- end -}}
{{- end -}}
{{- end -}}
