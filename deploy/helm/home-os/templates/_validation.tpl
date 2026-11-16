{{/*
Render-time invariants that must hold regardless of which optional objects are
enabled. Helm evaluates every template file, so a check that lives in an
optional template only runs when that template renders — which is exactly
backwards for an "at least one of these must be on" rule.
*/}}
{{- define "home-os.validate" -}}
{{- include "home-os.dexGrpcAssertCovered" . -}}
{{- end -}}
