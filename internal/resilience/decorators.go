package resilience

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
	"go-avatar-service/internal/storage"
)

// Breakers — брейкеры внешних зависимостей сервиса, по одному на зависимость:
// отказ S3 не должен размыкать доступ к базе и наоборот.
type Breakers struct {
	Postgres *Breaker
	S3       *Breaker
	RabbitMQ *Breaker
}

// NewBreakers создаёт брейкеры с общими порогами. Для каждой зависимости
// задано, какие ошибки считаются нормальным исходом, а не отказом:
// «запись не найдена» в БД и «нет объекта» в S3 — это ответы работающей
// зависимости, и поток 404 не должен выключать её.
func NewBreakers(failureThreshold uint32, openTimeout time.Duration) Breakers {
	settings := func(isBusiness func(error) bool) Settings {
		return Settings{FailureThreshold: failureThreshold, OpenTimeout: openTimeout, IsBusinessError: isBusiness}
	}
	return Breakers{
		Postgres: NewBreaker("postgres", settings(func(err error) bool {
			return errors.Is(err, repository.ErrNotFound)
		})),
		S3:       NewBreaker("s3", settings(storage.IsNotFound)),
		RabbitMQ: NewBreaker("rabbitmq", settings(nil)),
	}
}

// Декораторы оборачивают адаптеры внешних зависимостей брейкером и реализуют
// те же интерфейсы, что и оригиналы. Поэтому бизнес-логика (services, worker)
// о брейкерах ничего не знает — они подключаются при сборке в cmd/.

// --- S3 ---

type breakerStorage struct {
	inner storage.Storage
	b     *Breaker
}

// NewStorage оборачивает объектное хранилище брейкером.
func NewStorage(inner storage.Storage, b *Breaker) storage.Storage {
	return &breakerStorage{inner: inner, b: b}
}

func (s *breakerStorage) Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	return s.b.Execute(func() error { return s.inner.Upload(ctx, key, r, size, contentType) })
}

func (s *breakerStorage) Download(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	var (
		body io.ReadCloser
		size int64
	)
	err := s.b.Execute(func() error {
		var err error
		body, size, err = s.inner.Download(ctx, key)
		return err
	})
	return body, size, err
}

func (s *breakerStorage) Delete(ctx context.Context, key string) error {
	return s.b.Execute(func() error { return s.inner.Delete(ctx, key) })
}

func (s *breakerStorage) DeletePrefix(ctx context.Context, prefix string) error {
	return s.b.Execute(func() error { return s.inner.DeletePrefix(ctx, prefix) })
}

// Ping не проходит через брейкер: проверка здоровья должна видеть реальное
// состояние зависимости, а не состояние брейкера, иначе readiness-проба не
// заметила бы, что хранилище уже восстановилось.
func (s *breakerStorage) Ping(ctx context.Context) error {
	return s.inner.Ping(ctx)
}

// --- PostgreSQL ---

type breakerRepository struct {
	inner repository.AvatarRepository
	b     *Breaker
}

// NewRepository оборачивает репозиторий аватарок брейкером.
func NewRepository(inner repository.AvatarRepository, b *Breaker) repository.AvatarRepository {
	return &breakerRepository{inner: inner, b: b}
}

func (r *breakerRepository) Create(ctx context.Context, a domain.Avatar) (domain.Avatar, error) {
	var out domain.Avatar
	err := r.b.Execute(func() error {
		var err error
		out, err = r.inner.Create(ctx, a)
		return err
	})
	return out, err
}

func (r *breakerRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	var out domain.Avatar
	err := r.b.Execute(func() error {
		var err error
		out, err = r.inner.GetByID(ctx, id)
		return err
	})
	return out, err
}

func (r *breakerRepository) GetLatestByUserID(ctx context.Context, userID string) (domain.Avatar, error) {
	var out domain.Avatar
	err := r.b.Execute(func() error {
		var err error
		out, err = r.inner.GetLatestByUserID(ctx, userID)
		return err
	})
	return out, err
}

func (r *breakerRepository) ListByUserID(ctx context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error) {
	var (
		out   []domain.Avatar
		total int
	)
	err := r.b.Execute(func() error {
		var err error
		out, total, err = r.inner.ListByUserID(ctx, userID, limit, offset)
		return err
	})
	return out, total, err
}

func (r *breakerRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return r.b.Execute(func() error { return r.inner.SoftDelete(ctx, id) })
}

func (r *breakerRepository) UpdateProcessingStatus(ctx context.Context, id uuid.UUID, status string) error {
	return r.b.Execute(func() error { return r.inner.UpdateProcessingStatus(ctx, id, status) })
}

func (r *breakerRepository) UpdateThumbnails(ctx context.Context, id uuid.UUID, thumbnails map[string]string) error {
	return r.b.Execute(func() error { return r.inner.UpdateThumbnails(ctx, id, thumbnails) })
}

// --- RabbitMQ ---

// EventPublisher — то, что оборачивается брейкером на стороне брокера.
type EventPublisher interface {
	PublishProcessEvent(ctx context.Context, ev broker.AvatarProcessEvent) error
	PublishDeleteEvent(ctx context.Context, ev broker.AvatarDeleteEvent) error
}

type breakerPublisher struct {
	inner EventPublisher
	b     *Breaker
}

// NewPublisher оборачивает публикатор событий брейкером.
func NewPublisher(inner EventPublisher, b *Breaker) EventPublisher {
	return &breakerPublisher{inner: inner, b: b}
}

func (p *breakerPublisher) PublishProcessEvent(ctx context.Context, ev broker.AvatarProcessEvent) error {
	return p.b.Execute(func() error { return p.inner.PublishProcessEvent(ctx, ev) })
}

func (p *breakerPublisher) PublishDeleteEvent(ctx context.Context, ev broker.AvatarDeleteEvent) error {
	return p.b.Execute(func() error { return p.inner.PublishDeleteEvent(ctx, ev) })
}
