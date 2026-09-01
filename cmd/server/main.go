// Command server запускает HTTP API GophProfile: REST-эндпоинты для
// загрузки/получения/удаления аватарок и серверный веб-интерфейс.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/api"
	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/retryutil"
	"go-avatar-service/internal/services/avatar"
	"go-avatar-service/internal/storage"
	"go-avatar-service/internal/webui"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("ошибка загрузки конфигурации", "error", err)
		os.Exit(1)
	}

	var pool *pgxpool.Pool
	err = retryutil.Do(ctx, 5, 2*time.Second, func() error {
		var dialErr error
		pool, dialErr = postgres.NewPool(ctx, cfg.DatabaseURL)
		return dialErr
	})
	if err != nil {
		logger.Error("не удалось подключиться к PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := postgres.RunMigrations(cfg.DatabaseURL, "migrations"); err != nil {
		logger.Error("не удалось применить миграции", "error", err)
		os.Exit(1)
	}

	minioStorage, err := storage.NewMinioStorage(cfg.MinioEndpoint, cfg.MinioUser, cfg.MinioPassword, cfg.MinioUseSSL, cfg.MinioBucket)
	if err != nil {
		logger.Error("не удалось создать клиент MinIO", "error", err)
		os.Exit(1)
	}

	var amqpConn *amqp.Connection
	err = retryutil.Do(ctx, 5, 2*time.Second, func() error {
		var dialErr error
		amqpConn, dialErr = amqp.Dial(cfg.RabbitMQURL)
		return dialErr
	})
	if err != nil {
		logger.Error("не удалось подключиться к RabbitMQ", "error", err)
		os.Exit(1)
	}
	defer amqpConn.Close()

	amqpChannel, err := amqpConn.Channel()
	if err != nil {
		logger.Error("не удалось открыть канал RabbitMQ", "error", err)
		os.Exit(1)
	}
	defer amqpChannel.Close()

	if err := broker.DeclareTopology(amqpChannel); err != nil {
		logger.Error("не удалось объявить топологию RabbitMQ", "error", err)
		os.Exit(1)
	}

	repo := postgres.NewAvatarRepository(pool)
	publisher := broker.NewPublisher(amqpChannel)
	avatarService := avatar.NewService(repo, minioStorage, publisher)
	webHandlers := webui.NewHandlers(avatarService)

	server := api.NewAvatarServer(
		avatarService,
		webHandlers,
		pool,
		minioStorage,
		broker.NewConnectionPinger(amqpConn),
	)

	e := echo.New()
	e.HideBanner = true
	e.Static("/", "web/static")

	strictHandler := api.NewStrictHandler(server, nil)
	api.RegisterHandlers(e, strictHandler)

	go func() {
		logger.Info("сервер запущен", "port", cfg.ServerPort)
		if err := e.Start(":" + cfg.ServerPort); err != nil && err != http.ErrServerClosed {
			logger.Error("сервер остановился с ошибкой", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("получен сигнал остановки, завершаем работу")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		logger.Error("ошибка при остановке сервера", "error", err)
	}
}
