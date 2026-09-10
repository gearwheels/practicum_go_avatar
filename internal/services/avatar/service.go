// Package avatar содержит основную бизнес-логику сервиса: загрузку,
// получение, листинг и удаление аватарок. Пакет не знает про HTTP/Echo —
// он работает поверх интерфейсов репозитория, хранилища и брокера, что
// делает его тестируемым без поднятия реальной инфраструктуры.
package avatar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository"
)

// tracerName — имя инструментирующей библиотеки для спанов бизнес-логики.
const tracerName = "go-avatar-service/internal/services/avatar"

// tracer возвращает трейсер сервиса аватарок.
func tracer() trace.Tracer { return otel.Tracer(tracerName) }

// recordError помечает спан ошибкой — вызывается во всех ветках выхода по
// ошибке, чтобы в Jaeger такие спаны были видны красным.
func recordError(span trace.Span, err error, msg string) {
	span.RecordError(err)
	span.SetStatus(codes.Error, msg)
}

// ErrForbidden — попытка изменить чужую аватарку.
var ErrForbidden = errors.New("forbidden")

// ErrNotFound — аватарка не найдена (переиспользуем ошибку репозитория,
// чтобы вызывающему коду не нужно было знать про repository package).
var ErrNotFound = repository.ErrNotFound

// EventPublisher — то, что нужно сервису от брокера сообщений. Отдельный
// интерфейс (а не *broker.Publisher напрямую) позволяет подменять
// публикатор фейком в юнит-тестах.
type EventPublisher interface {
	PublishProcessEvent(ctx context.Context, ev broker.AvatarProcessEvent) error
	PublishDeleteEvent(ctx context.Context, ev broker.AvatarDeleteEvent) error
}

// Storage — то, что нужно сервису от объектного хранилища.
type Storage interface {
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// Service реализует бизнес-логику работы с аватарками.
type Service struct {
	repo      repository.AvatarRepository
	storage   Storage
	publisher EventPublisher
}

// NewService создаёт сервис аватарок.
func NewService(repo repository.AvatarRepository, storage Storage, publisher EventPublisher) *Service {
	return &Service{repo: repo, storage: storage, publisher: publisher}
}

// Upload сохраняет оригинал аватарки в S3, создаёт запись в БД и публикует
// событие для асинхронной генерации миниатюр.
func (s *Service) Upload(ctx context.Context, userID, fileName, mimeType string, size int64, r io.Reader) (domain.Avatar, error) {
	ctx, span := tracer().Start(ctx, "upload_avatar")
	defer span.End()

	span.SetAttributes(
		attribute.String("user_id", userID),
		attribute.String("file_name", fileName),
		attribute.Int64("file_size", size),
		attribute.String("mime_type", mimeType),
	)

	start := time.Now()
	var err error
	defer func() { observability.ObserveUpload(start, err) }()

	id := uuid.New()
	key := originalKey(id, mimeType)
	span.SetAttributes(attribute.String("avatar_id", id.String()))

	slog.InfoContext(ctx, "uploading avatar",
		"user_id", userID,
		"file_size", size,
		"mime_type", mimeType,
	)

	if err = s.storage.Upload(ctx, key, r, size, mimeType); err != nil {
		recordError(span, err, "загрузка оригинала в хранилище")
		return domain.Avatar{}, fmt.Errorf("загрузка оригинала в хранилище: %w", err)
	}

	avatarRecord := domain.Avatar{
		ID:               id,
		UserID:           userID,
		FileName:         fileName,
		MimeType:         mimeType,
		SizeBytes:        size,
		S3Key:            key,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
	avatarRecord, err = s.repo.Create(ctx, avatarRecord)
	if err != nil {
		recordError(span, err, "сохранение метаданных")
		return domain.Avatar{}, fmt.Errorf("сохранение метаданных: %w", err)
	}

	// Объём хранилища на пользователя — бизнес-KPI из ТЗ. Считаем от
	// фактически сохранённого оригинала.
	observability.StorageUsage.WithLabelValues(userID).Add(float64(size))

	if err = s.publisher.PublishProcessEvent(ctx, broker.AvatarProcessEvent{
		AvatarID: id,
		UserID:   userID,
		S3Key:    key,
	}); err != nil {
		// Оригинал уже сохранён и запись в БД создана — не откатываем
		// загрузку из-за сбоя публикации, миниатюры можно сгенерировать
		// позже вручную/повторной публикацией. Ошибку логирует вызывающий код.
		recordError(span, err, "публикация события обработки")
		return avatarRecord, fmt.Errorf("публикация события обработки: %w", err)
	}

	slog.InfoContext(ctx, "avatar uploaded", "avatar_id", id, "user_id", userID)
	return avatarRecord, nil
}

// GetMetadata возвращает метаданные аватарки по id.
func (s *Service) GetMetadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	ctx, span := tracer().Start(ctx, "get_avatar_metadata",
		trace.WithAttributes(attribute.String("avatar_id", id.String())))
	defer span.End()

	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		recordError(span, err, "чтение метаданных")
	}
	return a, err
}

// ImageResult — бинарные данные изображения и сведения для HTTP-ответа.
type ImageResult struct {
	Body          io.ReadCloser
	ContentType   string
	ContentLength int64
	ETag          string
}

// GetImage отдаёт бинарные данные аватарки по id в запрошенном размере.
// Если миниатюра запрошенного размера ещё не сгенерирована, отдаётся
// оригинал (см. решение по WebP/format в плане — параметр format влияет
// только на заявленный Content-Type, а не на перекодирование).
func (s *Service) GetImage(ctx context.Context, id uuid.UUID, size string) (ImageResult, error) {
	ctx, span := tracer().Start(ctx, "get_avatar_image",
		trace.WithAttributes(
			attribute.String("avatar_id", id.String()),
			attribute.String("size", size),
		))
	defer span.End()

	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		recordError(span, err, "чтение метаданных")
		observability.DownloadsTotal.WithLabelValues(size, observability.StatusError).Inc()
		return ImageResult{}, err
	}
	return s.downloadImage(ctx, span, a, size)
}

// GetImageForUser отдаёт текущую (последнюю) аватарку пользователя.
func (s *Service) GetImageForUser(ctx context.Context, userID, size string) (ImageResult, error) {
	ctx, span := tracer().Start(ctx, "get_user_avatar_image",
		trace.WithAttributes(
			attribute.String("user_id", userID),
			attribute.String("size", size),
		))
	defer span.End()

	a, err := s.repo.GetLatestByUserID(ctx, userID)
	if err != nil {
		recordError(span, err, "поиск текущей аватарки")
		observability.DownloadsTotal.WithLabelValues(size, observability.StatusError).Inc()
		return ImageResult{}, err
	}
	return s.downloadImage(ctx, span, a, size)
}

func (s *Service) downloadImage(ctx context.Context, span trace.Span, a domain.Avatar, size string) (ImageResult, error) {
	key := a.S3Key
	servedSize := "original"
	if size != "" && size != "original" {
		if thumbKey, ok := a.ThumbnailS3Keys[size]; ok {
			key = thumbKey
			servedSize = size
		}
		// Если миниатюра ещё не готова — отдаём оригинал, ничего не ломаем.
	}
	// Видно в трейсе, когда вместо запрошенной миниатюры ушёл оригинал.
	span.SetAttributes(attribute.String("served_size", servedSize))

	body, length, err := s.storage.Download(ctx, key)
	if err != nil {
		recordError(span, err, "скачивание из хранилища")
		observability.DownloadsTotal.WithLabelValues(size, observability.StatusError).Inc()
		return ImageResult{}, fmt.Errorf("скачивание из хранилища: %w", err)
	}

	observability.DownloadsTotal.WithLabelValues(size, observability.StatusSuccess).Inc()
	return ImageResult{
		Body:          body,
		ContentType:   a.MimeType,
		ContentLength: length,
		ETag:          etag(a),
	}, nil
}

// ListForUser возвращает страницу аватарок пользователя.
func (s *Service) ListForUser(ctx context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error) {
	ctx, span := tracer().Start(ctx, "list_user_avatars",
		trace.WithAttributes(
			attribute.String("user_id", userID),
			attribute.Int("limit", limit),
			attribute.Int("offset", offset),
		))
	defer span.End()

	avatars, total, err := s.repo.ListByUserID(ctx, userID, limit, offset)
	if err != nil {
		recordError(span, err, "выборка аватарок пользователя")
		return nil, 0, err
	}
	span.SetAttributes(attribute.Int("total", total))
	return avatars, total, nil
}

// DeleteByID мягко удаляет аватарку и публикует событие асинхронной очистки
// S3. requestingUserID должен совпадать с владельцем — иначе ErrForbidden.
func (s *Service) DeleteByID(ctx context.Context, id uuid.UUID, requestingUserID string) error {
	ctx, span := tracer().Start(ctx, "delete_avatar",
		trace.WithAttributes(
			attribute.String("avatar_id", id.String()),
			attribute.String("user_id", requestingUserID),
		))
	defer span.End()

	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		recordError(span, err, "чтение аватарки")
		observability.DeletesTotal.WithLabelValues(observability.StatusError).Inc()
		return err
	}
	if a.UserID != requestingUserID {
		recordError(span, ErrForbidden, "удаление чужой аватарки")
		observability.DeletesTotal.WithLabelValues(observability.StatusError).Inc()
		return ErrForbidden
	}
	return s.deleteAvatar(ctx, span, a)
}

// DeleteCurrentForUser удаляет текущую аватарку пользователя userID.
// requestingUserID (значение заголовка X-User-ID) должен совпадать с userID
// из пути — иначе ErrForbidden.
func (s *Service) DeleteCurrentForUser(ctx context.Context, userID, requestingUserID string) error {
	ctx, span := tracer().Start(ctx, "delete_user_avatar",
		trace.WithAttributes(attribute.String("user_id", userID)))
	defer span.End()

	if userID != requestingUserID {
		recordError(span, ErrForbidden, "удаление чужой аватарки")
		observability.DeletesTotal.WithLabelValues(observability.StatusError).Inc()
		return ErrForbidden
	}
	a, err := s.repo.GetLatestByUserID(ctx, userID)
	if err != nil {
		recordError(span, err, "поиск текущей аватарки")
		observability.DeletesTotal.WithLabelValues(observability.StatusError).Inc()
		return err
	}
	return s.deleteAvatar(ctx, span, a)
}

func (s *Service) deleteAvatar(ctx context.Context, span trace.Span, a domain.Avatar) error {
	if err := s.repo.SoftDelete(ctx, a.ID); err != nil {
		recordError(span, err, "мягкое удаление")
		observability.DeletesTotal.WithLabelValues(observability.StatusError).Inc()
		return err
	}

	// Освободившийся объём вычитаем из общей занятости пользователя.
	observability.StorageUsage.WithLabelValues(a.UserID).Sub(float64(a.SizeBytes))

	keys := []string{a.S3Key}
	for _, k := range a.ThumbnailS3Keys {
		keys = append(keys, k)
	}

	if err := s.publisher.PublishDeleteEvent(ctx, broker.AvatarDeleteEvent{
		AvatarID: a.ID,
		S3Keys:   keys,
	}); err != nil {
		recordError(span, err, "публикация события удаления")
		observability.DeletesTotal.WithLabelValues(observability.StatusError).Inc()
		return fmt.Errorf("публикация события удаления: %w", err)
	}

	observability.DeletesTotal.WithLabelValues(observability.StatusSuccess).Inc()
	slog.InfoContext(ctx, "avatar deleted", "avatar_id", a.ID, "user_id", a.UserID)
	return nil
}

// originalKey формирует ключ S3 для оригинала аватарки.
func originalKey(id uuid.UUID, mimeType string) string {
	return fmt.Sprintf("avatars/%s/original.%s", id, extForMime(mimeType))
}

// ThumbnailKey формирует ключ S3 для миниатюры заданного размера —
// используется воркером при генерации миниатюр.
func ThumbnailKey(id uuid.UUID, size, mimeType string) string {
	return fmt.Sprintf("avatars/%s/thumb_%s.%s", id, size, extForMime(mimeType))
}

func extForMime(mimeType string) string {
	switch mimeType {
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	default:
		return "jpg"
	}
}

// etag — дешёвый ETag на основе времени последнего обновления записи, без
// повторного чтения объекта из S3.
func etag(a domain.Avatar) string {
	return strconv.Quote(strconv.FormatInt(a.UpdatedAt.UnixNano(), 36))
}
