{{/*
Expand the name of the chart.
*/}}
{{- define "sealos-state-metrics.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "sealos-state-metrics.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "sealos-state-metrics.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "sealos-state-metrics.labels" -}}
helm.sh/chart: {{ include "sealos-state-metrics.chart" . }}
{{ include "sealos-state-metrics.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "sealos-state-metrics.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sealos-state-metrics.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Selector labels as a map for helper composition.
*/}}
{{- define "sealos-state-metrics.selectorLabelsMap" -}}
app.kubernetes.io/name: {{ include "sealos-state-metrics.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "sealos-state-metrics.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sealos-state-metrics.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Config controller name.
*/}}
{{- define "sealos-state-metrics.configControllerName" -}}
{{- printf "%s-config-controller" (include "sealos-state-metrics.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Config controller selector labels.
*/}}
{{- define "sealos-state-metrics.configControllerSelectorLabels" -}}
app.kubernetes.io/name: {{ include "sealos-state-metrics.name" . }}-config-controller
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Config controller common labels.
*/}}
{{- define "sealos-state-metrics.configControllerLabels" -}}
helm.sh/chart: {{ include "sealos-state-metrics.chart" . }}
{{ include "sealos-state-metrics.configControllerSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: config-controller
{{- end }}

{{/*
Config controller ServiceAccount name.
*/}}
{{- define "sealos-state-metrics.configControllerServiceAccountName" -}}
{{- if .Values.configController.serviceAccount.create }}
{{- default (include "sealos-state-metrics.configControllerName" .) .Values.configController.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.configController.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Return the proper image name
*/}}
{{- define "sealos-state-metrics.image" -}}
{{- .Values.image }}
{{- end }}

{{/*
Render the pod template shared by DaemonSet and Deployment workloads.
*/}}
{{- define "sealos-state-metrics.podTemplate" -}}
{{- $deploymentMode := .Values.deploymentMode | default "daemonset" -}}
{{- $affinity := deepCopy (.Values.affinity | default dict) -}}
{{- if and (eq $deploymentMode "deployment") (.Values.scheduling.preferDifferentNodes.enabled) -}}
{{- $selectorLabels := include "sealos-state-metrics.selectorLabelsMap" . | fromYaml -}}
{{- $podAntiAffinity := deepCopy (get $affinity "podAntiAffinity" | default dict) -}}
{{- $preferred := deepCopy (get $podAntiAffinity "preferredDuringSchedulingIgnoredDuringExecution" | default list) -}}
{{- $preferred = append $preferred (dict
  "weight" (.Values.scheduling.preferDifferentNodes.weight | int)
  "podAffinityTerm" (dict
    "labelSelector" (dict "matchLabels" $selectorLabels)
    "topologyKey" .Values.scheduling.preferDifferentNodes.topologyKey
  )
) -}}
{{- $_ := set $podAntiAffinity "preferredDuringSchedulingIgnoredDuringExecution" $preferred -}}
{{- $_ := set $affinity "podAntiAffinity" $podAntiAffinity -}}
{{- end -}}
metadata:
  annotations:
    checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
    {{- with .Values.podAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  labels:
    {{- include "sealos-state-metrics.selectorLabels" . | nindent 4 }}
spec:
  {{- with .Values.imagePullSecrets }}
  imagePullSecrets:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  serviceAccountName: {{ include "sealos-state-metrics.serviceAccountName" . }}
  securityContext:
    {{- toYaml .Values.podSecurityContext | nindent 4 }}
  {{- with .Values.priorityClassName }}
  priorityClassName: {{ . }}
  {{- end }}
  containers:
    - name: {{ .Chart.Name }}
      {{- if has "lvm" .Values.enabledCollectors }}
      securityContext:
        privileged: true
      {{- else }}
      securityContext:
        {{- toYaml .Values.securityContext | nindent 8 }}
      {{- end }}
      image: {{ include "sealos-state-metrics.image" . }}
      imagePullPolicy: {{ .Values.imagePullPolicy }}
      args:
        - -c
        - /config/config.yaml
      ports:
        - name: server
          containerPort: {{ .Values.service.targetPort }}
          protocol: TCP
        {{- if .Values.config.debugServer.enabled }}
        - name: debug
          containerPort: 8080
          protocol: TCP
        {{- end }}
        {{- if .Values.config.pprof.enabled }}
        - name: pprof
          containerPort: {{ .Values.config.pprof.port }}
          protocol: TCP
          hostPort: {{ .Values.config.pprof.port }}
        {{- end }}
      env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: LEADER_ELECTION_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: LEADER_ELECTION_LEASE_NAME
          value: {{ include "sealos-state-metrics.fullname" . }}
        {{- if .Values.tls.enabled }}
        - name: SERVER_TLS_ENABLED
          value: "true"
        - name: SERVER_TLS_CERT_FILE
          value: "/etc/tls/tls.crt"
        - name: SERVER_TLS_KEY_FILE
          value: "/etc/tls/tls.key"
        {{- end }}
        {{- if .Values.auth.enabled }}
        - name: SERVER_AUTH_ENABLED
          value: "true"
        {{- end }}
      volumeMounts:
        - name: config
          mountPath: /config
          readOnly: true
        {{- if .Values.tls.enabled }}
        - name: tls
          mountPath: /etc/tls
          readOnly: true
        {{- end }}
        {{- if has "lvm" .Values.enabledCollectors }}
        - name: dev
          mountPath: /dev
        - name: lvm-config
          mountPath: /etc/lvm
        - name: kubelet
          mountPath: /var/lib/kubelet
          mountPropagation: Bidirectional
        {{- end }}
      livenessProbe:
        httpGet:
          path: {{ .Values.livenessProbe.httpGet.path }}
          port: {{ .Values.livenessProbe.httpGet.port }}
          {{- if .Values.tls.enabled }}
          scheme: HTTPS
          {{- end }}
        initialDelaySeconds: {{ .Values.livenessProbe.initialDelaySeconds }}
        periodSeconds: {{ .Values.livenessProbe.periodSeconds }}
        timeoutSeconds: {{ .Values.livenessProbe.timeoutSeconds }}
        failureThreshold: {{ .Values.livenessProbe.failureThreshold }}
      readinessProbe:
        httpGet:
          path: {{ .Values.readinessProbe.httpGet.path }}
          port: {{ .Values.readinessProbe.httpGet.port }}
          {{- if .Values.tls.enabled }}
          scheme: HTTPS
          {{- end }}
        initialDelaySeconds: {{ .Values.readinessProbe.initialDelaySeconds }}
        periodSeconds: {{ .Values.readinessProbe.periodSeconds }}
        timeoutSeconds: {{ .Values.readinessProbe.timeoutSeconds }}
        failureThreshold: {{ .Values.readinessProbe.failureThreshold }}
      resources:
        {{- toYaml .Values.resources | nindent 8 }}
  volumes:
    - name: config
      configMap:
        name: {{ include "sealos-state-metrics.fullname" . }}
    {{- if .Values.tls.enabled }}
    - name: tls
      secret:
        secretName: {{ include "sealos-state-metrics.fullname" . }}-tls
        defaultMode: 0400
    {{- end }}
    {{- if has "lvm" .Values.enabledCollectors }}
    - name: dev
      hostPath:
        path: /dev
    - name: lvm-config
      hostPath:
        path: /etc/lvm
    - name: kubelet
      hostPath:
        path: /var/lib/kubelet
        type: Directory
    {{- end }}
  {{- with .Values.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- if gt (len $affinity) 0 }}
  affinity:
    {{- toYaml $affinity | nindent 4 }}
  {{- end }}
  {{- with .Values.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
