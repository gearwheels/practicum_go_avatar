// Package postgres содержит реализацию repository.AvatarRepository поверх
// pgx/v5 и вспомогательные функции для работы с пулом соединений и
// миграциями.
package postgres

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создаёт пул соединений с PostgreSQL по DSN.
//
// Пул конфигурируется через ParseConfig (а не pgxpool.New), чтобы повесить
// на соединения OTel-трейсер: каждый SQL-запрос попадает в трейс отдельным
// спаном.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("разбор DSN: %w", err)
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
