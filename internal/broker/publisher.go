package broker

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// tracerName — имя инструментирующей библиотеки для спанов брокера.
const tracerName = "go-avatar-service/internal/broker"

// Publisher публикует события аватарок в exchange avatars.events.
type Publisher struct {
	ch *amqp.Channel
}

// NewPublisher создаёт публикатор на уже открытом AMQP-канале.
func NewPublisher(ch *amqp.Channel) *Publisher {
	return &Publisher{ch: ch}
}

func (p *Publisher) PublishProcessEvent(ctx context.Context, ev AvatarProcessEvent) error {
	return p.publish(ctx, ProcessRoutingKey, ev)
}

func (p *Publisher) PublishDeleteEvent(ctx context.Context, ev AvatarDeleteEvent) error {
	return p.publish(ctx, DeleteRoutingKey, ev)
}

func (p *Publisher) publish(ctx context.Context, routingKey string, ev any) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "publish "+routingKey,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemRabbitmq,
			semconv.MessagingDestinationName(ExchangeName),
			semconv.MessagingRabbitmqDestinationRoutingKey(routingKey),
		),
	)
	defer span.End()

	body, err := json.Marshal(ev)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "сериализация события")
		return fmt.Errorf("сериализация события: %w", err)
	}

	// Кладём traceparent в заголовки сообщения, чтобы воркер продолжил тот
	// же трейс, а не начинал новый.
	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, amqpHeaderCarrier(headers))

	err = p.ch.PublishWithContext(ctx, ExchangeName, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Headers:      headers,
		Body:         body,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "публикация сообщения")
		return err
	}

	span.SetAttributes(attribute.Int("messaging.message.body.size", len(body)))
	return nil
}
