// Package resilience защищает сервис от каскадных отказов внешних
// зависимостей (PostgreSQL, S3, RabbitMQ) с помощью circuit breaker.
//
// Без него при недоступной зависимости каждый запрос честно ждёт таймаута
// соединения: горутины и соединения копятся, латентность растёт, и отказ
// одной зависимости превращается в отказ всего сервиса. Разомкнутый
// брейкер отвечает мгновенно, давая зависимости восстановиться, а
// периодически пропускает пробный запрос, чтобы замкнуться обратно.
package resilience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sony/gobreaker/v2"

	"go-avatar-service/internal/observability"
)

// ErrCircuitOpen — брейкер разомкнут, вызов к зависимости не выполнялся.
// HTTP-слой отвечает на неё 503: это временная недоступность, а не ошибка
// сервера.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// Settings — параметры брейкера.
type Settings struct {
	// FailureThreshold — сколько отказов подряд размыкают брейкер.
	FailureThreshold uint32
	// OpenTimeout — сколько брейкер остаётся разомкнутым перед пробным запросом.
	OpenTimeout time.Duration
	// IsBusinessError сообщает, что ошибка — нормальный исход операции
	// (например, «не найдено»), а не отказ зависимости. Такие ошибки не
	// приближают размыкание: иначе поток 404 выключил бы работающую базу.
	IsBusinessError func(error) bool
}

// Breaker — circuit breaker для одной внешней зависимости.
type Breaker struct {
	name string
	cb   *gobreaker.CircuitBreaker[struct{}]
}

// NewBreaker создаёт брейкер с именем name (оно же — метка в метриках).
func NewBreaker(name string, s Settings) *Breaker {
	if s.FailureThreshold == 0 {
		s.FailureThreshold = 5
	}
	if s.OpenTimeout <= 0 {
		s.OpenTimeout = 30 * time.Second
	}

	observability.SetCircuitBreakerState(name, int(gobreaker.StateClosed))

	cb := gobreaker.NewCircuitBreaker[struct{}](gobreaker.Settings{
		Name: name,
		// В полуоткрытом состоянии пропускаем один пробный запрос.
		MaxRequests: 1,
		Timeout:     s.OpenTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= s.FailureThreshold
		},
		OnStateChange: func(name string, _, to gobreaker.State) {
			observability.SetCircuitBreakerState(name, int(to))
		},
		// Отмена запроса клиентом — не отказ зависимости: не считаем её ни
		// успехом, ни провалом.
		IsExcluded: func(err error) bool {
			return errors.Is(err, context.Canceled)
		},
		IsSuccessful: func(err error) bool {
			return err == nil || (s.IsBusinessError != nil && s.IsBusinessError(err))
		},
	})

	return &Breaker{name: name, cb: cb}
}

// Execute выполняет fn через брейкер. Если брейкер разомкнут, fn не
// вызывается и возвращается ошибка, оборачивающая ErrCircuitOpen.
func (b *Breaker) Execute(fn func() error) error {
	_, err := b.cb.Execute(func() (struct{}, error) {
		return struct{}{}, fn()
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		observability.CircuitBreakerRejectedTotal.WithLabelValues(b.name).Inc()
		return fmt.Errorf("%s: %w", b.name, ErrCircuitOpen)
	}
	return err
}

// State возвращает текущее состояние: "closed", "half-open" или "open".
func (b *Breaker) State() string {
	return b.cb.State().String()
}
