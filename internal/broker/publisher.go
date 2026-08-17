package broker

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

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
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("сериализация события: %w", err)
	}

	return p.ch.PublishWithContext(ctx, ExchangeName, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}
