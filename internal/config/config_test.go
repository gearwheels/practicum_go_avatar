package config

import "testing"

func TestLoad_MissingRequiredVars(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RABBITMQ_URL", "")
	t.Setenv("MINIO_ENDPOINT", "")

	if _, err := Load(); err == nil {
		t.Fatal("ожидалась ошибка при отсутствующих обязательных переменных окружения")
	}
}

func TestLoad_Success(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	t.Setenv("MINIO_ENDPOINT", "localhost:9000")
	t.Setenv("SERVER_PORT", "9090")
	t.Setenv("MINIO_USE_SSL", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if cfg.ServerPort != "9090" {
		t.Errorf("ServerPort = %q, хотим 9090", cfg.ServerPort)
	}
	if !cfg.MinioUseSSL {
		t.Error("MinioUseSSL должен быть true")
	}
	if cfg.MinioBucket != "avatars" {
		t.Errorf("MinioBucket по умолчанию должен быть avatars, получено %q", cfg.MinioBucket)
	}
}
