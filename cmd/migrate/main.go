// Command migrate применяет миграции базы данных и завершается.
//
// Отдельный бинарь нужен для Kubernetes: там миграции накатывает Job-хук
// Helm до выката новых подов, а сами поды сервера стартуют уже с готовой
// схемой (RUN_MIGRATIONS=false). Так несколько реплик не стартуют
// наперегонки и не оставляют схему в состоянии dirty при сбое.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/retryutil"
)

const serviceName = "avatar-migrate"

// migrationsPath — путь относительно WORKDIR образа (см. docker/Dockerfile).
const migrationsPath = "migrations"

// Параметры ожидания базы: при установке «с нуля» под Postgres может быть
// ещё не готов принимать соединения, когда Job уже стартовал.
const (
	migrateAttempts = 30
	migrateDelay    = 2 * time.Second
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("ошибка загрузки конфигурации", "error", err)
		os.Exit(1)
	}

	logger := observability.NewLogger(os.Stdout, serviceName, cfg.LogLevel)
	slog.SetDefault(logger)

	logger.Info("применяем миграции")

	ctx := context.Background()
	err = retryutil.Do(ctx, migrateAttempts, migrateDelay, func() error {
		return postgres.RunMigrations(cfg.DatabaseURL, migrationsPath)
	})
	if err != nil {
		logger.Error("не удалось применить миграции", "error", err)
		os.Exit(1)
	}

	logger.Info("миграции применены")
}
