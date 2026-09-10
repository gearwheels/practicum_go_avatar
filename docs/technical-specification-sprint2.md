# Техническое задание на спринт 2 — наблюдаемость

## О спринте

У вас есть работающий MVP сервиса GophProfile. Он принимает запросы и выполняет бизнес-логику. Но в реальном продакшене этого недостаточно. Если сервис начнёт тормозить или отдавать 500-е ошибки, без качественных инструментов наблюдаемости вы не поймёте причину.

В этом спринте вы реализуете наблюдаемость: настроите мониторинг (Prometheus) и трейсинг (Jaeger), а для сбора логов сможете выбрать стек — Grafana Loki или OpenSearch/ELK. Выбирайте тот, который вам интереснее освоить.

## Задачи второго этапа разработки GophProfile

### 1. Внедрить инструменты наблюдаемости

Инструментировать код приложения с помощью OpenTelemetry: реализовать распределённый трейсинг (HTTP, БД, S3, брокер), сбор технических и бизнес-метрик, а также настроить структурированное логирование (slog) с корреляцией логов и трейсов.

#### 1.1 Трейсинг

- Инструментирование HTTP-запросов
- Трейсы для работы с БД
- Трейсы для S3-операций
- Трейсы для брокера сообщений
- Context propagation между сервисами

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
)

func (s *AvatarService) UploadAvatar(ctx context.Context, req *UploadRequest) error {
    ctx, span := otel.Tracer("avatar-service").Start(ctx, "upload_avatar")
    defer span.End()

    span.SetAttributes(
        attribute.String("user_id", req.UserID),
        attribute.String("file_name", req.FileName),
        attribute.Int64("file_size", req.Size),
    )
    // ...
}
```

#### 1.2 Метрики

```go
var (
    uploadsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "avatars_uploads_total",
            Help: "Total number of avatar uploads",
        },
        []string{"status"},
    )

    uploadDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "avatars_upload_duration_seconds",
            Help: "Avatar upload duration",
        },
        []string{"status"},
    )

    storageUsage = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "avatars_storage_bytes",
            Help: "Total storage used by avatars",
        },
        []string{"user_id"},
    )
)
```

#### 1.3 Логирование

- Структурированные логи (JSON)
- Корреляция с trace ID
- Уровни логирования
- Использование slog

```go
import "log/slog"

logger := slog.With(
    "service", "avatar-service",
    "trace_id", trace.SpanFromContext(ctx).SpanContext().TraceID(),
)

logger.Info("uploading avatar",
    "user_id", userID,
    "file_size", fileSize,
    "mime_type", mimeType,
)
```

### 2. Развернуть инфраструктуру мониторинга и логирования

Подключить и настроить внешний стек сервисов: Prometheus для сбора метрик, Jaeger для трейсинга, Grafana для визуализации, а также систему сбора логов (Grafana Loki или OpenSearch/ELK).

#### 2.1 Метрики Prometheus

- HTTP-метрики (requests, duration, errors)
- Бизнес-метрики (uploads, storage usage)
- Инфраструктурные метрики (DB connections, queue depth)

#### 2.2 Jaeger

- Distributed tracing
- Performance анализ
- Dependency mapping

#### 2.3 OpenSearch/ELK

- Централизованное логирование
- Алерты на ошибки
- Log aggregation и поиск

### 3. Настроить визуализацию

Создать информативные дашборды в Grafana:

- Service overview
- Request rate, error rate, duration (RED metrics)
- Resource utilization
- Business KPIs

### 4. Бонусная задача: настроить алертинг

Сконфигурируйте правила для Prometheus Alertmanager, чтобы он реагировал на критические показатели: высокий процент ошибок и увеличенное время ответа.

Пример правил:

```yaml
groups:
- name: avatar-service
  rules:
  - alert: HighErrorRate
    expr: rate(avatars_uploads_total{status="error"}[5m]) / rate(avatars_uploads_total[5m]) > 0.1
    for: 5m
    labels:
      severity: warning

  - alert: HighResponseTime
    expr: histogram_quantile(0.95, avatars_upload_duration_seconds) > 5
    for: 2m
    labels:
      severity: critical
```

---

## Как это реализовано в проекте

Ниже — краткая карта соответствия требований и кода.

### Принятые решения

- **Стек логов:** Grafana Loki + Promtail (вариант из пункта 2.3 на выбор).
- **Трейсы идут в Jaeger напрямую по OTLP**, без промежуточного OTel Collector: Jaeger all-in-one сам принимает OTLP на порту 4317, для проекта такого масштаба лишний хоп не нужен.
- **Бонусная задача 4 (Alertmanager) не выполнялась.**

### Трейсинг (1.1)

| Требование | Где реализовано |
|---|---|
| HTTP-запросы | `otelecho.Middleware` в [cmd/server/main.go](../cmd/server/main.go) |
| Бизнес-логика | Ручные спаны с атрибутами в [internal/services/avatar/service.go](../internal/services/avatar/service.go) |
| БД | `otelpgx.NewTracer()` в [internal/repository/postgres/pool.go](../internal/repository/postgres/pool.go) |
| S3 | `otelhttp.NewTransport` в [internal/storage/minio.go](../internal/storage/minio.go) |
| Брокер | Спаны продюсера/консьюмера в [internal/broker/publisher.go](../internal/broker/publisher.go) и [consumer.go](../internal/broker/consumer.go) |
| Context propagation | `traceparent` в заголовках AMQP через [internal/broker/carrier.go](../internal/broker/carrier.go) |

Благодаря последнему пункту трейс не обрывается на брокере: загрузка аватарки в `avatar-server` и последующая генерация миниатюр в `avatar-worker` попадают в один трейс.

### Метрики (1.2, 2.1)

Определены в [internal/observability/metrics.go](../internal/observability/metrics.go).

- **Бизнесовые:** `avatars_uploads_total{status}`, `avatars_upload_duration_seconds{status}`, `avatars_storage_bytes{user_id}` (ровно как в ТЗ), плюс `avatars_deletes_total`, `avatars_downloads_total`, `avatars_thumbnails_generated_total`.
- **HTTP (RED):** `avatar_requests_total`, `avatar_request_duration_seconds` — middleware `echoprometheus`.
- **Инфраструктурные:** соединения пула БД (`db_pool_*`, собственный коллектор поверх `pgxpool.Stat()`), глубина очередей — из метрик самого RabbitMQ (порт 15692), метрики MinIO — с `/minio/v2/metrics/cluster`.

Эндпоинты: `http://localhost:8080/metrics` (сервер) и `http://localhost:9091/metrics` (воркер, своего API у него нет).

### Логирование (1.3)

[internal/observability/logging.go](../internal/observability/logging.go) — обёртка над `slog.JSONHandler`, которая достаёт `SpanContext` из `context.Context` и дописывает `trace_id`/`span_id`.

Важная деталь: корреляция работает только с контекстными методами (`InfoContext`/`ErrorContext`) — обычные `slog.Info` передают внутрь `context.Background()`, в котором спана нет. Поэтому все места логирования в проекте переведены на контекстные варианты.

### Инфраструктура (2.1, 2.2, 2.3)

Сервисы `jaeger`, `prometheus`, `loki`, `promtail`, `grafana` в [docker-compose.yml](../docker-compose.yml); конфигурация — в каталоге [monitoring/](../monitoring).

В датасорсах Grafana настроена двусторонняя связка: из строки лога в Loki можно провалиться в трейс Jaeger (`derivedFields` по `trace_id`), а из спана — в логи того же трейса.

### Визуализация (3)

Дашборд [monitoring/grafana/dashboards/avatar-service.json](../monitoring/grafana/dashboards/avatar-service.json) с рядами: Service overview, RED metrics, Resource utilization, Business KPIs и отдельный ряд по асинхронной обработке.
