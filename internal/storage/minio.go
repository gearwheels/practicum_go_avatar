// Package storage отвечает за хранение бинарных файлов (оригиналов и
// миниатюр аватарок) в S3-совместимом хранилище.
package storage

import (
	"context"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Storage — интерфейс доступа к объектному хранилищу.
type Storage interface {
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (io.ReadCloser, int64, error)
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) error
	Ping(ctx context.Context) error
}

// MinioStorage — реализация Storage поверх MinIO SDK.
type MinioStorage struct {
	client *minio.Client
	bucket string
}

// NewMinioStorage создаёт клиента MinIO для заданного бакета. Сам бакет
// должен быть создан заранее (см. сервис minio-init в docker-compose.yml).
//
// HTTP-транспорт обёрнут otelhttp: каждая операция с S3 попадает в трейс
// отдельным клиентским спаном без ручной разметки в методах ниже.
func NewMinioStorage(endpoint, accessKey, secretKey string, useSSL bool, bucket string) (*MinioStorage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:    useSSL,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	})
	if err != nil {
		return nil, err
	}
	return &MinioStorage{client: client, bucket: bucket}, nil
}

func (s *MinioStorage) Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

func (s *MinioStorage) Download(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, err
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, err
	}
	return obj, info.Size, nil
}

func (s *MinioStorage) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

// DeletePrefix удаляет все объекты с заданным префиксом (например, все
// файлы одной аватарки: оригинал + миниатюры). Удаление несуществующих
// объектов в S3 — не ошибка, поэтому операция идемпотентна.
func (s *MinioStorage) DeletePrefix(ctx context.Context, prefix string) error {
	objectsCh := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	for obj := range objectsCh {
		if obj.Err != nil {
			return obj.Err
		}
		if err := s.client.RemoveObject(ctx, s.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// Ping проверяет доступность хранилища и существование бакета — используется
// в health-check.
func (s *MinioStorage) Ping(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		return &BucketNotFoundError{Bucket: s.bucket}
	}
	return nil
}

// BucketNotFoundError — бакет, настроенный в конфигурации, не существует.
type BucketNotFoundError struct {
	Bucket string
}

func (e *BucketNotFoundError) Error() string {
	return "bucket not found: " + e.Bucket
}
