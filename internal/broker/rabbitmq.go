package broker

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// Имена топологии RabbitMQ. Единый topic-exchange для всех событий аватарок;
// отдельная очередь на "загрузку" не нужна — оригинал сохраняется синхронно
// в HTTP-хендлере, асинхронно обрабатываются только генерация миниатюр и
// удаление файлов из S3.
const (
	ExchangeName = "avatars.events"

	ProcessQueue      = "avatar.process.queue"
	ProcessRoutingKey = "avatar.process"

	DeleteQueue      = "avatar.delete.queue"
	DeleteRoutingKey = "avatar.delete"
)

// DeclareTopology объявляет exchange, рабочие очереди и dead-letter очереди
// для них. Вызывается при старте и сервера, и воркера — оба могут
// подняться первыми, объявление идемпотентно.
func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(ExchangeName, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return err
	}

	for _, q := range []struct {
		name       string
		routingKey string
	}{
		{ProcessQueue, ProcessRoutingKey},
		{DeleteQueue, DeleteRoutingKey},
	} {
		dlqName := q.name + ".dlq"

		// Dead-letter очередь: сюда попадают сообщения, которые воркер не
		// смог обработать за отведённое число попыток (см. internal/worker).
		if _, err := ch.QueueDeclare(dlqName, true, false, false, false, nil); err != nil {
			return err
		}

		// Рабочая очередь: при явном reject (requeue=false) сообщение
		// уходит в одноимённую dead-letter очередь через default exchange
		// (routing key = имя очереди при паблише в default exchange).
		_, err := ch.QueueDeclare(q.name, true, false, false, false, amqp.Table{
			"x-dead-letter-exchange":    "",
			"x-dead-letter-routing-key": dlqName,
		})
		if err != nil {
			return err
		}

		if err := ch.QueueBind(q.name, q.routingKey, ExchangeName, false, nil); err != nil {
			return err
		}
	}

	return nil
}
