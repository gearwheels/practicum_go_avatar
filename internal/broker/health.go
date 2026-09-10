package broker

import (
	"context"
	"errors"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ConnectionPinger оборачивает *amqp.Connection интерфейсом Pinger
// (context.Context, error) для использования в HealthCheck.
type ConnectionPinger struct {
	conn *amqp.Connection
}

// NewConnectionPinger создаёт обёртку для проверки соединения с RabbitMQ.
func NewConnectionPinger(conn *amqp.Connection) *ConnectionPinger {
	return &ConnectionPinger{conn: conn}
}

// Ping возвращает ошибку, если соединение закрыто.
func (p *ConnectionPinger) Ping(_ context.Context) error {
	if p.conn == nil || p.conn.IsClosed() {
		return errors.New("amqp connection is closed")
	}
	return nil
}
