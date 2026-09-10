package broker

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// amqpHeaderCarrier реализует propagation.TextMapCarrier поверх заголовков
// AMQP. Через него traceparent записывается в сообщение при публикации и
// читается при получении — благодаря этому трейс, начатый в HTTP-запросе к
// серверу, продолжается в воркере.
type amqpHeaderCarrier amqp.Table

// Get возвращает значение заголовка. Значения в amqp.Table имеют тип any,
// поэтому строкой считается только то, что действительно строка.
func (c amqpHeaderCarrier) Get(key string) string {
	v, ok := c[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func (c amqpHeaderCarrier) Set(key, value string) {
	c[key] = value
}

func (c amqpHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
