package broker

import (
	"context"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
)

// MaxRetries — сколько раз воркер пытается обработать сообщение прежде
// чем отправить его в dead-letter очередь.
const MaxRetries = 3

const retryCountHeader = "x-retry-count"

// HandlerFunc обрабатывает тело сообщения. Ошибка означает "не удалось,
// нужно повторить" (см. Consume).
type HandlerFunc func(ctx context.Context, body []byte) error

// Consumer читает сообщения из очереди и передаёт их в HandlerFunc с учётом
// ограниченного числа повторов.
type Consumer struct {
	ch *amqp.Channel
}

// NewConsumer создаёт потребителя на уже открытом AMQP-канале.
func NewConsumer(ch *amqp.Channel) *Consumer {
	return &Consumer{ch: ch}
}

// Consume блокирующе читает сообщения из очереди queue до отмены ctx.
// При ошибке обработчика сообщение переотправляется в ту же очередь со
// счётчиком попыток в заголовке; после MaxRetries попыток сообщение
// отклоняется без requeue и, согласно топологии (см. DeclareTopology),
// уходит в dead-letter очередь.
func (c *Consumer) Consume(ctx context.Context, queue string, handler HandlerFunc) error {
	if err := c.ch.Qos(1, 0, false); err != nil {
		return err
	}

	deliveries, err := c.ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return nil
			}
			c.handleDelivery(ctx, queue, d, handler)
		}
	}
}

func (c *Consumer) handleDelivery(ctx context.Context, queue string, d amqp.Delivery, handler HandlerFunc) {
	err := handler(ctx, d.Body)
	if err == nil {
		if ackErr := d.Ack(false); ackErr != nil {
			slog.Error("не удалось подтвердить сообщение", "queue", queue, "error", ackErr)
		}
		return
	}

	slog.Error("ошибка обработки сообщения", "queue", queue, "error", err)

	retryCount := retryCountFromHeaders(d.Headers)
	if retryCount >= MaxRetries {
		// Попытки исчерпаны — отклоняем без requeue, сообщение уходит в DLQ.
		if nackErr := d.Nack(false, false); nackErr != nil {
			slog.Error("не удалось отклонить сообщение", "queue", queue, "error", nackErr)
		}
		return
	}

	headers := amqp.Table{}
	for k, v := range d.Headers {
		headers[k] = v
	}
	headers[retryCountHeader] = retryCount + 1

	republishErr := c.ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
		ContentType:  d.ContentType,
		DeliveryMode: amqp.Persistent,
		Headers:      headers,
		Body:         d.Body,
	})
	if republishErr != nil {
		slog.Error("не удалось переотправить сообщение на повтор", "queue", queue, "error", republishErr)
		_ = d.Nack(false, true)
		return
	}
	_ = d.Ack(false)
}

func retryCountFromHeaders(headers amqp.Table) int {
	v, ok := headers[retryCountHeader]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int32:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
