{{/*
Базовое имя чарта.
*/}}
{{- define "gophprofile.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Полное имя релиза — префикс для всех ресурсов.
*/}}
{{- define "gophprofile.fullname" -}}
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

{{/*
Общие метки для всех ресурсов чарта.
*/}}
{{- define "gophprofile.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: gophprofile
{{- end -}}

{{/*
Селекторные метки конкретного компонента.
Вызов: {{ include "gophprofile.selectorLabels" (dict "ctx" . "component" "server") }}
*/}}
{{- define "gophprofile.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gophprofile.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Имя ServiceAccount.
*/}}
{{- define "gophprofile.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "gophprofile.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Имя Secret с учётными данными: либо созданный чартом, либо внешний.
*/}}
{{- define "gophprofile.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-secrets" (include "gophprofile.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Пароли. Дефолтов нет: required останавливает рендер, если пароль не задан,
— это дешевле, чем обнаружить в рабочем окружении учётные данные из
репозитория. Вызываются только там, где пароль действительно нужен, поэтому
установка с готовым Secret (secrets.existingSecret) или с готовым DSN
работает без них.
*/}}
{{- define "gophprofile.postgresPassword" -}}
{{- required "secrets.postgresPassword не задан: передайте пароль (--set secrets.postgresPassword=...) или подключите готовый Secret через secrets.existingSecret" .Values.secrets.postgresPassword -}}
{{- end -}}

{{- define "gophprofile.rabbitmqPassword" -}}
{{- required "secrets.rabbitmqPassword не задан: передайте пароль (--set secrets.rabbitmqPassword=...) или подключите готовый Secret через secrets.existingSecret" .Values.secrets.rabbitmqPassword -}}
{{- end -}}

{{- define "gophprofile.minioPassword" -}}
{{- required "secrets.minioPassword не задан: передайте пароль (--set secrets.minioPassword=...) или подключите готовый Secret через secrets.existingSecret" .Values.secrets.minioPassword -}}
{{- end -}}

{{/*
Имена сервисов инфраструктуры.
*/}}
{{- define "gophprofile.postgresServiceName" -}}
{{- printf "%s-postgres" (include "gophprofile.fullname" .) -}}
{{- end -}}

{{- define "gophprofile.rabbitmqServiceName" -}}
{{- printf "%s-rabbitmq" (include "gophprofile.fullname" .) -}}
{{- end -}}

{{- define "gophprofile.minioServiceName" -}}
{{- printf "%s-minio" (include "gophprofile.fullname" .) -}}
{{- end -}}

{{/*
DSN PostgreSQL. Если инфраструктура в кластере — собираем адрес из имени
сервиса; если внешняя — берём готовый DSN из values (обязателен).
*/}}
{{- define "gophprofile.databaseUrl" -}}
{{- if .Values.secrets.databaseUrl -}}
{{- .Values.secrets.databaseUrl -}}
{{- else if .Values.infra.postgres.enabled -}}
{{- printf "postgres://%s:%s@%s:5432/%s?sslmode=disable"
      .Values.secrets.postgresUser
      (include "gophprofile.postgresPassword" .)
      (include "gophprofile.postgresServiceName" .)
      .Values.secrets.postgresDatabase -}}
{{- else -}}
{{- fail "infra.postgres.enabled=false: задайте secrets.databaseUrl или secrets.existingSecret" -}}
{{- end -}}
{{- end -}}

{{/*
URL RabbitMQ — та же логика, что и для DSN базы.
*/}}
{{- define "gophprofile.rabbitmqUrl" -}}
{{- if .Values.secrets.rabbitmqUrl -}}
{{- .Values.secrets.rabbitmqUrl -}}
{{- else if .Values.infra.rabbitmq.enabled -}}
{{- printf "amqp://%s:%s@%s:5672/"
      .Values.secrets.rabbitmqUser
      (include "gophprofile.rabbitmqPassword" .)
      (include "gophprofile.rabbitmqServiceName" .) -}}
{{- else -}}
{{- fail "infra.rabbitmq.enabled=false: задайте secrets.rabbitmqUrl или secrets.existingSecret" -}}
{{- end -}}
{{- end -}}

{{/*
Адрес MinIO для приложения (host:port без схемы — так его ждёт minio-go).
*/}}
{{- define "gophprofile.minioEndpoint" -}}
{{- printf "%s:9000" (include "gophprofile.minioServiceName" .) -}}
{{- end -}}
