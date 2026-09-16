// Package worker содержит обработчики фоновых событий (генерация миниатюр,
// удаление файлов из S3), которые запускает cmd/worker.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/imaging"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository"
	"go-avatar-service/internal/services/avatar"
)

// tracerName — имя инструментирующей библиотеки для спанов воркера.
const tracerName = "go-avatar-service/internal/worker"

func tracer() trace.Tracer { return otel.Tracer(tracerName) }

// Storage — то, что нужно воркеру от объектного хранилища.
type Storage interface {
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (io.ReadCloser, int64, error)
	DeletePrefix(ctx context.Context, prefix string) error
}

// Handler обрабатывает события генерации миниатюр и удаления файлов.
type Handler struct {
	repo    repository.AvatarRepository
	storage Storage
}

// New создаёт обработчик фоновых событий.
func New(repo repository.AvatarRepository, storage Storage) *Handler {
	return &Handler{repo: repo, storage: storage}
}

// HandleProcess генерирует миниатюры для аватарки из события
// AvatarProcessEvent. Идемпотентен: если обработка уже завершена
// (processing_status = completed), сообщение просто подтверждается без
// повторной работы.
func (h *Handler) HandleProcess(ctx context.Context, body []byte) error {
	ctx, span := tracer().Start(ctx, "process_avatar")
	defer span.End()

	var ev broker.AvatarProcessEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "разбор события")
		return fmt.Errorf("разбор события обработки: %w", err)
	}
	span.SetAttributes(
		attribute.String("avatar_id", ev.AvatarID.String()),
		attribute.String("user_id", ev.UserID),
	)

	a, err := h.repo.GetByID(ctx, ev.AvatarID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Аватарка удалена до того, как дошла очередь до обработки —
			// не ошибка, просто нечего делать.
			span.SetAttributes(attribute.String("skip_reason", "not_found"))
			slog.InfoContext(ctx, "аватарка не найдена, пропускаем обработку", "avatar_id", ev.AvatarID)
			return nil
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "чтение аватарки")
		return fmt.Errorf("чтение аватарки: %w", err)
	}

	if a.ProcessingStatus == domain.ProcessingStatusCompleted {
		// Идемпотентность: повторная доставка того же сообщения не должна
		// заново гонять генерацию миниатюр.
		span.SetAttributes(attribute.String("skip_reason", "already_completed"))
		slog.InfoContext(ctx, "аватарка уже обработана, пропускаем", "avatar_id", ev.AvatarID)
		return nil
	}

	if err := h.generateThumbnails(ctx, a); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "генерация миниатюр")
		observability.ThumbnailsGeneratedTotal.WithLabelValues(observability.StatusError).Inc()
		_ = h.repo.UpdateProcessingStatus(ctx, a.ID, domain.ProcessingStatusFailed)
		return err
	}

	observability.ThumbnailsGeneratedTotal.WithLabelValues(observability.StatusSuccess).Inc()
	return nil
}

func (h *Handler) generateThumbnails(ctx context.Context, a domain.Avatar) error {
	ctx, span := tracer().Start(ctx, "generate_thumbnails",
		trace.WithAttributes(attribute.String("avatar_id", a.ID.String())))
	defer span.End()

	start := time.Now()
	defer func() { observability.ThumbnailDuration.Observe(time.Since(start).Seconds()) }()

	_ = h.repo.UpdateProcessingStatus(ctx, a.ID, domain.ProcessingStatusProcessing)

	original, _, err := h.storage.Download(ctx, a.S3Key)
	if err != nil {
		return fmt.Errorf("скачивание оригинала: %w", err)
	}
	defer original.Close()

	src, err := imaging.Decode(original)
	if err != nil {
		return fmt.Errorf("декодирование оригинала: %w", err)
	}

	thumbMime := imaging.EncodeMimeType(a.MimeType)
	thumbnails := imaging.Generate(src)
	keys := make(map[string]string, len(thumbnails))

	for label, img := range thumbnails {
		key := avatar.ThumbnailKey(a.ID, label, thumbMime)

		var buf bytes.Buffer
		if err := imaging.Encode(&buf, img, thumbMime); err != nil {
			return fmt.Errorf("кодирование миниатюры %s: %w", label, err)
		}
		if err := h.storage.Upload(ctx, key, &buf, int64(buf.Len()), thumbMime); err != nil {
			return fmt.Errorf("загрузка миниатюры %s: %w", label, err)
		}
		keys[label] = key
	}

	if err := h.repo.UpdateThumbnails(ctx, a.ID, keys); err != nil {
		return fmt.Errorf("сохранение ключей миниатюр: %w", err)
	}
	if err := h.repo.UpdateProcessingStatus(ctx, a.ID, domain.ProcessingStatusCompleted); err != nil {
		return fmt.Errorf("обновление статуса обработки: %w", err)
	}

	span.SetAttributes(attribute.Int("thumbnails_count", len(keys)))
	slog.InfoContext(ctx, "миниатюры сгенерированы", "avatar_id", a.ID, "thumbnails_count", len(keys))
	return nil
}

// HandleDelete удаляет все файлы аватарки из S3 по событию
// AvatarDeleteEvent. Идемпотентен: удаление отсутствующих объектов в S3 не
// является ошибкой.
func (h *Handler) HandleDelete(ctx context.Context, body []byte) error {
	ctx, span := tracer().Start(ctx, "delete_avatar_files")
	defer span.End()

	var ev broker.AvatarDeleteEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "разбор события")
		return fmt.Errorf("разбор события удаления: %w", err)
	}
	span.SetAttributes(attribute.String("avatar_id", ev.AvatarID.String()))

	prefix := fmt.Sprintf("avatars/%s/", ev.AvatarID)
	if err := h.storage.DeletePrefix(ctx, prefix); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "удаление файлов из хранилища")
		return fmt.Errorf("удаление файлов аватарки из хранилища: %w", err)
	}

	slog.InfoContext(ctx, "файлы аватарки удалены", "avatar_id", ev.AvatarID)
	return nil
}
