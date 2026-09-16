// Package config отвечает за загрузку конфигурации приложения из переменных
// окружения (см. .env.example).
package config

import (
	"fmt"
	"os"
)

// Config — конфигурация, общая для server и worker. Поле ServerPort нужно
// только серверу.
type Config struct {
	ServerPort string

	DatabaseURL string

	RabbitMQURL string

	MinioEndpoint string
	MinioUser     string
	MinioPassword string
	MinioUseSSL   bool
	MinioBucket   string

	// Наблюдаемость. Все поля опциональны: с пустым OTLPEndpoint трейсинг
	// просто выключается, и сервис работает без Jaeger.
	OTLPEndpoint string
	MetricsPort  string
	LogLevel     string

	// RunMigrations включает прогон миграций при старте сервера. По
	// умолчанию true — так работает docker-compose с одним экземпляром.
	// В Kubernetes выключается: там миграции накатывает отдельный Job
	// (Helm-хук), иначе несколько реплик стартуют наперегонки.
	RunMigrations bool
}

// Load читает конфигурацию из переменных окружения. Возвращает ошибку, если
// не заданы обязательные для работы значения.
func Load() (Config, error) {
	cfg := Config{
		ServerPort:    getEnv("SERVER_PORT", "8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		RabbitMQURL:   os.Getenv("RABBITMQ_URL"),
		MinioEndpoint: os.Getenv("MINIO_ENDPOINT"),
		MinioUser:     os.Getenv("MINIO_ROOT_USER"),
		MinioPassword: os.Getenv("MINIO_ROOT_PASSWORD"),
		MinioUseSSL:   getEnv("MINIO_USE_SSL", "false") == "true",
		MinioBucket:   getEnv("MINIO_BUCKET", "avatars"),
		// Без fallback: пустое значение должно выключать трейсинг. С
		// getEnv("...", "jaeger:4317") пустая строка означала бы «не задано»,
		// и сервис в кластере без Jaeger бесконечно ломился бы по этому
		// адресу, а при остановке писал ошибку экспорта.
		OTLPEndpoint:  os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		MetricsPort:   getEnv("METRICS_PORT", "9091"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		RunMigrations: getEnv("RUN_MIGRATIONS", "true") != "false",
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if cfg.RabbitMQURL == "" {
		missing = append(missing, "RABBITMQ_URL")
	}
	if cfg.MinioEndpoint == "" {
		missing = append(missing, "MINIO_ENDPOINT")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("не заданы обязательные переменные окружения: %v", missing)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
