package retryutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDo_SucceedsAfterFailures(t *testing.T) {
	attempts := 0
	err := Do(context.Background(), 5, time.Millisecond, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("пока не готово")
		}
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 3, attempts)
}

func TestDo_ReturnsLastErrorWhenExhausted(t *testing.T) {
	wantErr := errors.New("так и не подключились")
	attempts := 0

	err := Do(context.Background(), 3, time.Millisecond, func() error {
		attempts++
		return wantErr
	})

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 3, attempts)
}

func TestDo_StopsEarlyWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	done := make(chan error, 1)
	go func() {
		done <- Do(ctx, 100, 50*time.Millisecond, func() error {
			attempts++
			if attempts == 1 {
				cancel()
			}
			return errors.New("всегда падает")
		})
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Do не остановился после отмены контекста")
	}

	require.Less(t, attempts, 100, "Do должен был прерваться задолго до исчерпания всех попыток")
}
