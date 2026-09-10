// Command worker потребляет события из RabbitMQ и выполняет фоновую
// обработку аватарок: генерацию миниатюр и удаление файлов из S3.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/retryutil"
	"go-avatar-service/internal/storage"
	"go-avatar-service/internal/worker"
)

// serviceName — под этим именем воркер виден в Jaeger и в логах.
const serviceName = "avatar-worker"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("ошибка загрузки конфигурации", "error", err)
		os.Exit(1)
	}

	logger := observability.NewLogger(os.Stdout, serviceName, cfg.LogLevel)
	slog.SetDefault(logger)

	shutdownTracing, err := observability.InitTracing(ctx, serviceName, cfg.OTLPEndpoint)
	if err != nil {
		logger.Error("не удалось инициализировать трейсинг", "error", err)
		os.Exit(1)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(flushCtx); err != nil {
			logger.Error("ошибка остановки трейсинга", "error", err)
		}
	}()

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

	if err := observability.RegisterDBPoolMetrics(pool); err != nil {
		logger.Error("не удалось зарегистрировать метрики пула БД", "error", err)
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

	// У воркера нет своего API, но метрики Prometheus нужно откуда-то
	// забирать — поднимаем минимальный HTTP-сервер только под /metrics.
	metricsServer := &http.Server{
		Addr:              ":" + cfg.MetricsPort,
		Handler:           metricsMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("метрики воркера доступны", "port", cfg.MetricsPort)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("сервер метрик остановился с ошибкой", "error", err)
		}
	}()

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("ошибка при остановке сервера метрик", "error", err)
	}

	wg.Wait()
}

func metricsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", observability.Handler())
	return mux
}
