// Package postgres содержит реализацию repository.AvatarRepository поверх
// pgx/v5 и вспомогательные функции для работы с пулом соединений и
// миграциями.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создаёт пул соединений с PostgreSQL по DSN.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
