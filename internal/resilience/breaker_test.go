package resilience

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/observability"
)

var errDependencyDown = errors.New("connection refused")

// testOpenTimeout — сколько брейкер в тестах остаётся разомкнутым. Реального
// ожидания не возникает: тесты двигают время через fakeClock.
const testOpenTimeout = 50 * time.Millisecond

// fakeClock — управляемый источник времени. Благодаря ему проверка перехода
// open → half-open не зависит от планировщика ОС и загрузки машины.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testBreaker(t *testing.T, name string, isBusiness func(error) bool) *Breaker {
	t.Helper()
	b, _ := testBreakerWithClock(t, name, isBusiness)
	return b
}

func testBreakerWithClock(t *testing.T, name string, isBusiness func(error) bool) (*Breaker, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	return NewBreaker(name, Settings{
		FailureThreshold: 3,
		OpenTimeout:      testOpenTimeout,
		IsBusinessError:  isBusiness,
		Clock:            clock,
	}), clock
}

func TestBreaker_OpensAfterConsecutiveFailures(t *testing.T) {
	b := testBreaker(t, "test-open", nil)

	for i := 0; i < 3; i++ {
		err := b.Execute(func() error { return errDependencyDown })
		require.ErrorIs(t, err, errDependencyDown, "до порога возвращается настоящая ошибка зависимости")
	}
	require.Equal(t, "open", b.State())

	// Разомкнутый брейкер не вызывает зависимость вообще.
	called := false
	err := b.Execute(func() error { called = true; return nil })
	require.ErrorIs(t, err, ErrCircuitOpen)
	require.False(t, called, "при разомкнутом брейкере зависимость вызываться не должна")
}

func TestBreaker_OpenFailsFast(t *testing.T) {
	b := testBreaker(t, "test-fast", nil)
	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}

	start := time.Now()
	err := b.Execute(func() error {
		time.Sleep(time.Second) // «зависшая» зависимость
		return nil
	})
	require.ErrorIs(t, err, ErrCircuitOpen)
	require.Less(t, time.Since(start), 50*time.Millisecond, "отказ должен быть мгновенным, без ожидания зависимости")
}

// Бизнес-ошибки («не найдено») — нормальный ответ работающей зависимости:
// сколько бы их ни было, брейкер остаётся замкнутым.
func TestBreaker_BusinessErrorsDoNotTrip(t *testing.T) {
	errNotFound := errors.New("not found")
	b := testBreaker(t, "test-business", func(err error) bool { return errors.Is(err, errNotFound) })

	for i := 0; i < 10; i++ {
		err := b.Execute(func() error { return errNotFound })
		require.ErrorIs(t, err, errNotFound)
	}
	require.Equal(t, "closed", b.State())
}

func TestBreaker_ClientCancellationDoesNotTrip(t *testing.T) {
	b := testBreaker(t, "test-cancel", nil)
	for i := 0; i < 10; i++ {
		_ = b.Execute(func() error { return context.Canceled })
	}
	require.Equal(t, "closed", b.State())
}

// После OpenTimeout брейкер пропускает пробный запрос; если зависимость
// ожила — замыкается и работает как прежде.
func TestBreaker_RecoversAfterTimeout(t *testing.T) {
	b, clock := testBreakerWithClock(t, "test-recover", nil)
	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}
	require.Equal(t, "open", b.State())

	// До истечения таймаута брейкер остаётся разомкнутым.
	clock.Advance(testOpenTimeout - time.Nanosecond)
	require.Equal(t, "open", b.State())

	clock.Advance(time.Nanosecond)
	require.Equal(t, "half-open", b.State())

	require.NoError(t, b.Execute(func() error { return nil }))
	require.Equal(t, "closed", b.State())
}

// В полуоткрытом состоянии к зависимости уходит ровно один пробный запрос:
// она ещё не признана живой, и пускать на неё весь трафик рано.
func TestBreaker_HalfOpenAllowsSingleProbe(t *testing.T) {
	b, clock := testBreakerWithClock(t, "test-half-open", nil)
	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}
	clock.Advance(testOpenTimeout)

	probeStarted := make(chan struct{})
	probeMayFinish := make(chan struct{})
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		_ = b.Execute(func() error {
			close(probeStarted)
			<-probeMayFinish
			return nil
		})
	}()
	<-probeStarted

	called := false
	err := b.Execute(func() error { called = true; return nil })
	require.ErrorIs(t, err, ErrCircuitOpen)
	require.False(t, called, "второй запрос в half-open к зависимости не уходит")

	close(probeMayFinish)
	<-probeDone
	require.Equal(t, "closed", b.State())
}

// Неудачный пробный запрос снова размыкает брейкер, и отсчёт OpenTimeout
// начинается заново — иначе после первой же неудачной пробы зависимость
// получила бы поток запросов.
func TestBreaker_FailedProbeReopens(t *testing.T) {
	b, clock := testBreakerWithClock(t, "test-probe-fails", nil)
	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}
	clock.Advance(testOpenTimeout)
	require.Equal(t, "half-open", b.State())

	require.ErrorIs(t, b.Execute(func() error { return errDependencyDown }), errDependencyDown)
	require.Equal(t, "open", b.State())

	clock.Advance(testOpenTimeout - time.Nanosecond)
	require.Equal(t, "open", b.State(), "таймаут отсчитывается заново от неудачной пробы")

	clock.Advance(time.Nanosecond)
	require.Equal(t, "half-open", b.State())
}

// Клиент отменил запрос во время пробы — это не ответ зависимости, поэтому
// брейкер не замыкается, но и не размыкается: пробу нужно повторить.
func TestBreaker_CancelledProbeKeepsHalfOpen(t *testing.T) {
	b, clock := testBreakerWithClock(t, "test-probe-cancelled", nil)
	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}
	clock.Advance(testOpenTimeout)

	require.ErrorIs(t, b.Execute(func() error { return context.Canceled }), context.Canceled)
	require.Equal(t, "half-open", b.State())

	require.NoError(t, b.Execute(func() error { return nil }))
	require.Equal(t, "closed", b.State())
}

// Паника обработчика — такой же отказ зависимости, как и ошибка. Если бы
// брейкер её не учитывал, занятый пробный слот half-open не освободился бы
// и брейкер больше никогда не замкнулся бы.
func TestBreaker_PanicCountsAsFailure(t *testing.T) {
	b, _ := testBreakerWithClock(t, "test-panic", nil)

	for i := 0; i < 3; i++ {
		require.Panics(t, func() {
			_ = b.Execute(func() error { panic("зависимость развалилась") })
		})
	}
	require.Equal(t, "open", b.State())
}

func TestBreaker_ExportsStateAndRejections(t *testing.T) {
	b := testBreaker(t, "test-metrics", nil)

	// Счётчик — глобальный для процесса, поэтому сравниваем прирост, а не
	// абсолютное значение: иначе тест прошёл бы только с первого раза.
	rejected := observability.CircuitBreakerRejectedTotal.WithLabelValues("test-metrics")
	before := testutil.ToFloat64(rejected)

	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}
	require.Equal(t, "open", b.State())

	called := false
	_ = b.Execute(func() error { called = true; return nil })
	require.False(t, called, "отклонённый вызов до зависимости не доходит")

	require.Equal(t, before+1, testutil.ToFloat64(rejected))
}

// --- декоратор хранилища ---

type failingStorage struct{ calls int }

func (s *failingStorage) Upload(context.Context, string, io.Reader, int64, string) error {
	s.calls++
	return errDependencyDown
}
func (s *failingStorage) Download(context.Context, string) (io.ReadCloser, int64, error) {
	s.calls++
	return nil, 0, errDependencyDown
}
func (s *failingStorage) Delete(context.Context, string) error { s.calls++; return errDependencyDown }
func (s *failingStorage) DeletePrefix(context.Context, string) error {
	s.calls++
	return errDependencyDown
}
func (s *failingStorage) Ping(context.Context) error { return errDependencyDown }

func TestStorageDecorator_OpensAndBypassesPing(t *testing.T) {
	inner := &failingStorage{}
	st := NewStorage(inner, testBreaker(t, "test-storage", nil))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.ErrorIs(t, st.Upload(ctx, "k", nil, 0, "image/png"), errDependencyDown)
	}
	callsBefore := inner.calls

	_, _, err := st.Download(ctx, "k")
	require.ErrorIs(t, err, ErrCircuitOpen)
	require.Equal(t, callsBefore, inner.calls, "разомкнутый брейкер не должен обращаться к хранилищу")

	// Проверка здоровья видит реальное состояние, а не ошибку брейкера.
	require.ErrorIs(t, st.Ping(ctx), errDependencyDown)
	require.NotErrorIs(t, st.Ping(ctx), ErrCircuitOpen)
}
