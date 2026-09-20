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
	"sync"
	"time"

	"go-avatar-service/internal/observability"
)

// ErrCircuitOpen — брейкер разомкнут, вызов к зависимости не выполнялся.
// HTTP-слой отвечает на неё 503: это временная недоступность, а не ошибка
// сервера.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// state — состояние брейкера. Числовые значения попадают в метрику
// circuit_breaker_state и подписаны на дашборде Grafana, поэтому менять их
// нельзя.
type state int

const (
	stateClosed   state = 0
	stateHalfOpen state = 1
	stateOpen     state = 2
)

func (s state) String() string {
	switch s {
	case stateHalfOpen:
		return "half-open"
	case stateOpen:
		return "open"
	default:
		return "closed"
	}
}

// Clock — источник времени брейкера.
//
// Вынесен в зависимость ради тестов: переход open → half-open происходит по
// истечении OpenTimeout, и без подменяемых часов проверка восстановления
// сводилась бы к time.Sleep — то есть зависела бы от загруженности машины.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

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
	// Clock подменяет источник времени. Пусто — системное время.
	Clock Clock
}

// Breaker — circuit breaker для одной внешней зависимости.
//
// Состояния и переходы между ними:
//
//	closed    — вызовы проходят; FailureThreshold отказов подряд → open
//	open      — вызовы отклоняются мгновенно; через OpenTimeout → half-open
//	half-open — пропускается ровно один пробный вызов; успех → closed,
//	            отказ → open (и отсчёт OpenTimeout начинается заново)
type Breaker struct {
	name             string
	failureThreshold uint32
	openTimeout      time.Duration
	isBusinessError  func(error) bool
	clock            Clock

	mu sync.Mutex
	// state читается и меняется только через currentState/setState, чтобы
	// метрика не разъезжалась с реальным состоянием.
	state state
	// consecutiveFailures — отказы подряд в состоянии closed.
	consecutiveFailures uint32
	// openedAt — момент размыкания, от него отсчитывается OpenTimeout.
	openedAt time.Time
	// probeInFlight — пробный вызов в half-open уже выполняется. Остальные
	// вызовы отклоняются: зависимость ещё не признана живой, и пускать на
	// неё весь трафик рано.
	probeInFlight bool
}

// NewBreaker создаёт брейкер с именем name (оно же — метка в метриках).
func NewBreaker(name string, s Settings) *Breaker {
	if s.FailureThreshold == 0 {
		s.FailureThreshold = 5
	}
	if s.OpenTimeout <= 0 {
		s.OpenTimeout = 30 * time.Second
	}
	if s.Clock == nil {
		s.Clock = systemClock{}
	}

	observability.SetCircuitBreakerState(name, int(stateClosed))

	return &Breaker{
		name:             name,
		failureThreshold: s.FailureThreshold,
		openTimeout:      s.OpenTimeout,
		isBusinessError:  s.IsBusinessError,
		clock:            s.Clock,
		state:            stateClosed,
	}
}

// Execute выполняет fn через брейкер. Если брейкер разомкнут, fn не
// вызывается и возвращается ошибка, оборачивающая ErrCircuitOpen.
func (b *Breaker) Execute(fn func() error) error {
	if err := b.beforeRequest(); err != nil {
		return err
	}

	// Паника обработчика — тоже отказ зависимости. Без этого defer пробный
	// слот half-open остался бы занятым навсегда, и брейкер больше никогда
	// не замкнулся бы.
	done := false
	defer func() {
		if !done {
			b.afterRequest(errPanic)
		}
	}()

	err := fn()
	done = true
	b.afterRequest(err)
	return err
}

// errPanic отмечает вызов, прерванный паникой: обычной ошибкой она не
// является, но для брейкера это отказ зависимости.
var errPanic = errors.New("panic in breaker call")

// beforeRequest решает, пускать ли вызов к зависимости.
func (b *Breaker) beforeRequest() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.currentStateLocked() {
	case stateOpen:
		return b.rejectLocked()
	case stateHalfOpen:
		if b.probeInFlight {
			return b.rejectLocked()
		}
		b.probeInFlight = true
	}
	return nil
}

// afterRequest учитывает исход вызова и при необходимости меняет состояние.
func (b *Breaker) afterRequest(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	wasProbe := b.probeInFlight
	b.probeInFlight = false

	switch {
	// Отмена запроса клиентом — не отказ зависимости: не считаем её ни
	// успехом, ни провалом.
	case errors.Is(err, context.Canceled):
		return
	case err == nil || (b.isBusinessError != nil && b.isBusinessError(err)):
		b.consecutiveFailures = 0
		if wasProbe {
			// Зависимость ответила — возвращаем весь трафик.
			b.setStateLocked(stateClosed)
		}
	default:
		b.consecutiveFailures++
		if wasProbe || b.consecutiveFailures >= b.failureThreshold {
			b.trip()
		}
	}
}

// trip размыкает брейкер и заново запускает отсчёт OpenTimeout.
func (b *Breaker) trip() {
	b.openedAt = b.clock.Now()
	b.setStateLocked(stateOpen)
}

// rejectLocked фиксирует отклонённый вызов в метрике и формирует ошибку.
func (b *Breaker) rejectLocked() error {
	observability.CircuitBreakerRejectedTotal.WithLabelValues(b.name).Inc()
	return fmt.Errorf("%s: %w", b.name, ErrCircuitOpen)
}

// currentStateLocked возвращает состояние с учётом истёкшего OpenTimeout:
// переход open → half-open происходит лениво, при обращении, а не по таймеру.
func (b *Breaker) currentStateLocked() state {
	if b.state == stateOpen && b.clock.Now().Sub(b.openedAt) >= b.openTimeout {
		b.setStateLocked(stateHalfOpen)
	}
	return b.state
}

func (b *Breaker) setStateLocked(s state) {
	if b.state == s {
		return
	}
	b.state = s
	observability.SetCircuitBreakerState(b.name, int(s))
}

// State возвращает текущее состояние: "closed", "half-open" или "open".
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentStateLocked().String()
}
