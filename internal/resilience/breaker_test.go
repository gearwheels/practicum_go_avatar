package resilience

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/observability"
)

var errDependencyDown = errors.New("connection refused")

func testBreaker(t *testing.T, name string, isBusiness func(error) bool) *Breaker {
	t.Helper()
	return NewBreaker(name, Settings{
		FailureThreshold: 3,
		OpenTimeout:      50 * time.Millisecond,
		IsBusinessError:  isBusiness,
	})
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
	b := testBreaker(t, "test-recover", nil)
	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}
	require.Equal(t, "open", b.State())

	time.Sleep(70 * time.Millisecond)
	require.Equal(t, "half-open", b.State())

	require.NoError(t, b.Execute(func() error { return nil }))
	require.Equal(t, "closed", b.State())
}

func TestBreaker_ExportsStateAndRejections(t *testing.T) {
	b := testBreaker(t, "test-metrics", nil)
	require.Equal(t, 0.0, testutil.ToFloat64(observability.CircuitBreakerRejectedTotal.WithLabelValues("test-metrics")))

	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errDependencyDown })
	}
	_ = b.Execute(func() error { return nil })

	require.Equal(t, 1.0, testutil.ToFloat64(observability.CircuitBreakerRejectedTotal.WithLabelValues("test-metrics")))
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
