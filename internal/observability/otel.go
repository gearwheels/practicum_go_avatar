// Package observability собирает в одном месте всю обвязку наблюдаемости:
// инициализацию OpenTelemetry-трейсинга, метрики Prometheus и логгер slog с
// корреляцией по trace_id.
package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ServiceVersion — версия, с которой сервисы регистрируются в Jaeger.
const ServiceVersion = "1.0.0"

// ShutdownFunc корректно останавливает провайдер трейсов, досылая всё, что
// осталось в буфере экспортёра.
type ShutdownFunc func(context.Context) error

// InitTracing настраивает глобальный TracerProvider с экспортом по OTLP/gRPC
// и W3C-распространением контекста (traceparent).
//
// Если endpoint пустой, трейсинг не включается: возвращается no-op shutdown,
// а приложение продолжает работать. Это важно для локального запуска и тестов
// без поднятого Jaeger.
func InitTracing(ctx context.Context, serviceName, endpoint string) (ShutdownFunc, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	// WithInsecure: внутри docker-сети трейсы идут открытым текстом, TLS тут
	// не нужен.
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("создание OTLP-экспортёра: %w", err)
	}

	// NewSchemaless, а не NewWithAttributes: у resource.Default() свой
	// SchemaURL, привязанный к версии SDK, и слияние двух ресурсов с разными
	// схемами возвращает ошибку. Ресурс без схемы сливается с любым.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("описание ресурса: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(provider)

	// TraceContext + Baggage — стандартный W3C-набор. Именно он позволяет
	// продолжить трейс в воркере, получив traceparent из заголовков AMQP
	// (см. internal/broker/carrier.go).
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return provider.Shutdown, nil
}
