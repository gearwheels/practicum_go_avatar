package broker

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestAMQPHeaderCarrier_SetGetKeys(t *testing.T) {
	headers := amqp.Table{}
	carrier := amqpHeaderCarrier(headers)

	carrier.Set("traceparent", "00-abc-def-01")

	require.Equal(t, "00-abc-def-01", carrier.Get("traceparent"))
	require.Equal(t, []string{"traceparent"}, carrier.Keys())
	require.Equal(t, "00-abc-def-01", headers["traceparent"])
}

func TestAMQPHeaderCarrier_GetMissingOrNonString(t *testing.T) {
	// В amqp.Table значения имеют тип any: числовой x-retry-count не должен
	// приводить к панике при чтении как строки.
	carrier := amqpHeaderCarrier(amqp.Table{"x-retry-count": int32(2)})

	require.Empty(t, carrier.Get("отсутствует"))
	require.Empty(t, carrier.Get("x-retry-count"))
}

// Ключевая проверка распространения контекста: traceparent, записанный на
// стороне публикации, восстанавливает тот же trace ID на стороне получения.
func TestAMQPHeaderCarrier_PropagatesTraceContext(t *testing.T) {
	propagator := propagation.TraceContext{}

	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	producerCtx, span := provider.Tracer("test").Start(context.Background(), "publish")
	defer span.End()

	headers := amqp.Table{}
	propagator.Inject(producerCtx, amqpHeaderCarrier(headers))
	require.Contains(t, headers, "traceparent")

	// На стороне воркера контекст пустой — он есть только в заголовках.
	consumerCtx := propagator.Extract(context.Background(), amqpHeaderCarrier(headers))

	extracted := trace.SpanContextFromContext(consumerCtx)
	require.True(t, extracted.IsValid())
	require.Equal(t, span.SpanContext().TraceID(), extracted.TraceID())
	require.Equal(t, span.SpanContext().SpanID(), extracted.SpanID())
}
