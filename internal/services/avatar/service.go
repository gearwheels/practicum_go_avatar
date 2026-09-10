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
	"strconv"

	"github.com/google/uuid"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
)

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
	id := uuid.New()
	key := originalKey(id, mimeType)

	if err := s.storage.Upload(ctx, key, r, size, mimeType); err != nil {
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
	avatarRecord, err := s.repo.Create(ctx, avatarRecord)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("сохранение метаданных: %w", err)
	}

	if err := s.publisher.PublishProcessEvent(ctx, broker.AvatarProcessEvent{
		AvatarID: id,
		UserID:   userID,
		S3Key:    key,
	}); err != nil {
		// Оригинал уже сохранён и запись в БД создана — не откатываем
		// загрузку из-за сбоя публикации, миниатюры можно сгенерировать
		// позже вручную/повторной публикацией. Ошибку логирует вызывающий код.
		return avatarRecord, fmt.Errorf("публикация события обработки: %w", err)
	}

	return avatarRecord, nil
}

// GetMetadata возвращает метаданные аватарки по id.
func (s *Service) GetMetadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	return s.repo.GetByID(ctx, id)
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
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return ImageResult{}, err
	}
	return s.downloadImage(ctx, a, size)
}

// GetImageForUser отдаёт текущую (последнюю) аватарку пользователя.
func (s *Service) GetImageForUser(ctx context.Context, userID, size string) (ImageResult, error) {
	a, err := s.repo.GetLatestByUserID(ctx, userID)
	if err != nil {
		return ImageResult{}, err
	}
	return s.downloadImage(ctx, a, size)
}

func (s *Service) downloadImage(ctx context.Context, a domain.Avatar, size string) (ImageResult, error) {
	key := a.S3Key
	if size != "" && size != "original" {
		if thumbKey, ok := a.ThumbnailS3Keys[size]; ok {
			key = thumbKey
		}
		// Если миниатюра ещё не готова — отдаём оригинал, ничего не ломаем.
	}

	body, length, err := s.storage.Download(ctx, key)
	if err != nil {
		return ImageResult{}, fmt.Errorf("скачивание из хранилища: %w", err)
	}

	return ImageResult{
		Body:          body,
		ContentType:   a.MimeType,
		ContentLength: length,
		ETag:          etag(a),
	}, nil
}

// ListForUser возвращает страницу аватарок пользователя.
func (s *Service) ListForUser(ctx context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error) {
	return s.repo.ListByUserID(ctx, userID, limit, offset)
}

// DeleteByID мягко удаляет аватарку и публикует событие асинхронной очистки
// S3. requestingUserID должен совпадать с владельцем — иначе ErrForbidden.
func (s *Service) DeleteByID(ctx context.Context, id uuid.UUID, requestingUserID string) error {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if a.UserID != requestingUserID {
		return ErrForbidden
	}
	return s.deleteAvatar(ctx, a)
}

// DeleteCurrentForUser удаляет текущую аватарку пользователя userID.
// requestingUserID (значение заголовка X-User-ID) должен совпадать с userID
// из пути — иначе ErrForbidden.
func (s *Service) DeleteCurrentForUser(ctx context.Context, userID, requestingUserID string) error {
	if userID != requestingUserID {
		return ErrForbidden
	}
	a, err := s.repo.GetLatestByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.deleteAvatar(ctx, a)
}

func (s *Service) deleteAvatar(ctx context.Context, a domain.Avatar) error {
	if err := s.repo.SoftDelete(ctx, a.ID); err != nil {
		return err
	}

	keys := []string{a.S3Key}
	for _, k := range a.ThumbnailS3Keys {
		keys = append(keys, k)
	}

	if err := s.publisher.PublishDeleteEvent(ctx, broker.AvatarDeleteEvent{
		AvatarID: a.ID,
		S3Keys:   keys,
	}); err != nil {
		return fmt.Errorf("публикация события удаления: %w", err)
	}
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
