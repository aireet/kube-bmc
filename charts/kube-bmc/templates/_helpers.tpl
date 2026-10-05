{{- define "kube-bmc.name" -}}kube-bmc{{- end -}}

{{- define "kube-bmc.fullname" -}}
{{- if contains "kube-bmc" .Release.Name -}}{{ .Release.Name | trunc 50 | trimSuffix "-" }}{{- else -}}{{ printf "%s-kube-bmc" .Release.Name | trunc 50 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "kube-bmc.labels" -}}
app.kubernetes.io/name: kube-bmc
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "kube-bmc.selector" -}}
app.kubernetes.io/name: kube-bmc
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kube-bmc.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default (printf "v%s" .Chart.AppVersion) }}
{{- end -}}

{{- define "kube-bmc.credentialsSecret" -}}
{{- if .Values.server.credentials.existingSecret -}}{{ .Values.server.credentials.existingSecret }}{{- else if .Values.server.credentials.create -}}{{ include "kube-bmc.fullname" . }}-credentials{{- end -}}
{{- end -}}

{{- define "kube-bmc.authSecret" -}}
{{- .Values.auth.existingSecret | default (printf "%s-auth" (include "kube-bmc.fullname" .)) -}}
{{- end -}}
