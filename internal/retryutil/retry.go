// Package retryutil содержит общий хелпер повторных попыток, используемый
// cmd/server и cmd/worker при подключении к внешним зависимостям на старте.
package retryutil

import (
	"context"
	"time"
)

// Do вызывает fn до attempts раз с паузой delay между попытками. Если ctx
// отменяется — в том числе во время паузы — Do возвращает ctx.Err()
// немедленно, не дожидаясь конца текущей паузы или исчерпания attempts.
func Do(ctx context.Context, attempts int, delay time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return err
}
