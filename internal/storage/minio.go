// Package storage отвечает за хранение бинарных файлов (оригиналов и
// миниатюр аватарок) в S3-совместимом хранилище.
package storage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

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

const (
	// dialTimeout — установка TCP/TLS-соединения с S3.
	dialTimeout = 5 * time.Second
	// responseHeaderTimeout — ожидание ответа S3 после отправки запроса
	// (тело при загрузке уже отправлено, поэтому размер файла не влияет).
	responseHeaderTimeout = 15 * time.Second
	maxRetries            = 3
)

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
//
// Таймауты транспорта обязательны: без них при недоступном S3 (пакеты
// теряются, а не отклоняются) запрос висит, пока клиент не оборвёт
// соединение, а circuit breaker не получает ошибку и не размыкается.
func NewMinioStorage(endpoint, accessKey, secretKey string, useSSL bool, bucket string) (*MinioStorage, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = dialTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout

	client, err := minio.New(endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:    useSSL,
		Transport: otelhttp.NewTransport(transport),
		// По умолчанию minio-go повторяет запрос 10 раз — при отказе S3 это
		// многократно растягивает ожидание. Короткие сбои переживут и 3
		// попытки, а длительные — задача circuit breaker.
		MaxRetries: maxRetries,
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

// IsNotFound сообщает, что запрошенного объекта нет в хранилище. Это
// нормальный исход операции, а не отказ S3 — circuit breaker не должен на
// нём размыкаться.
func IsNotFound(err error) bool {
	var resp minio.ErrorResponse
	return errors.As(err, &resp) && resp.Code == "NoSuchKey"
}
