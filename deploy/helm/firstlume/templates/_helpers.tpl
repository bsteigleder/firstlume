{{- define "firstlume.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "firstlume.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "firstlume.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "firstlume.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{- define "firstlume.executorImage" -}}
{{- $repo := .Values.agent.executor.image.repository | default .Values.image.repository -}}
{{- $tag := .Values.agent.executor.image.tag | default (.Values.image.tag | default .Chart.AppVersion) -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}

{{- define "firstlume.controlPlaneSecret" -}}
{{- .Values.controlPlane.existingSecret | default (printf "%s-control-plane" (include "firstlume.fullname" .)) -}}
{{- end -}}

{{- define "firstlume.executorNamespace" -}}
{{- .Values.agent.executor.namespace | default .Release.Namespace -}}
{{- end -}}

{{- define "firstlume.executorServiceAccount" -}}
{{- .Values.agent.executor.serviceAccount.name | default (printf "%s-executor" (include "firstlume.fullname" .)) -}}
{{- end -}}

{{- define "firstlume.apiUrl" -}}
{{- if .Values.agent.apiUrl -}}
{{- .Values.agent.apiUrl -}}
{{- else if .Values.controlPlane.enabled -}}
{{- printf "http://%s.%s.svc:%v" (include "firstlume.fullname" .) .Release.Namespace .Values.controlPlane.service.port -}}
{{- else -}}
{{- fail "agent.apiUrl is required when controlPlane.enabled=false" -}}
{{- end -}}
{{- end -}}

{{/* Returns an existing secret value, or a new random one, so values survive upgrades. */}}
{{- define "firstlume.persistedSecret" -}}
{{- $existing := lookup "v1" "Secret" .namespace .name -}}
{{- if and $existing (index $existing.data .key) -}}
{{- index $existing.data .key -}}
{{- else -}}
{{- .generate | b64enc -}}
{{- end -}}
{{- end -}}

{{- define "firstlume.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "firstlume.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}
