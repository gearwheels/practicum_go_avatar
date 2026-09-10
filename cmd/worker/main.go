// Command worker потребляет события из RabbitMQ и выполняет фоновую
// обработку аватарок: генерацию миниатюр и удаление файлов из S3.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/retryutil"
	"go-avatar-service/internal/storage"
	"go-avatar-service/internal/worker"
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
	handler := worker.New(repo, minioStorage)
	consumer := broker.NewConsumer(amqpChannel)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := consumer.Consume(ctx, broker.ProcessQueue, handler.HandleProcess); err != nil {
			logger.Error("остановлен консьюмер очереди обработки", "error", err)
		}
	}()

	go func() {
		defer wg.Done()
		if err := consumer.Consume(ctx, broker.DeleteQueue, handler.HandleDelete); err != nil {
			logger.Error("остановлен консьюмер очереди удаления", "error", err)
		}
	}()

	logger.Info("воркер запущен")
	<-ctx.Done()
	logger.Info("получен сигнал остановки, завершаем работу")
	wg.Wait()
}
