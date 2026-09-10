// Package domain содержит основные бизнес-сущности сервиса, не зависящие
// от конкретных фреймворков (HTTP, БД, брокера).
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Статусы загрузки оригинала аватарки в S3.
const (
	UploadStatusUploading = "uploading"
	UploadStatusUploaded  = "uploaded"
	UploadStatusFailed    = "failed"
)

// Статусы асинхронной обработки (генерации миниатюр).
const (
	ProcessingStatusPending    = "pending"
	ProcessingStatusProcessing = "processing"
	ProcessingStatusCompleted  = "completed"
	ProcessingStatusFailed     = "failed"
)

// Avatar — доменная модель аватарки, соответствует строке таблицы avatars.
type Avatar struct {
	ID               uuid.UUID
	UserID           string
	FileName         string
	MimeType         string
	SizeBytes        int64
	S3Key            string
	ThumbnailS3Keys  map[string]string // размер ("100x100") -> ключ в S3
	UploadStatus     string
	ProcessingStatus string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// ThumbnailSizes — поддерживаемые размеры миниатюр (см. ТЗ: 100x100, 300x300).
var ThumbnailSizes = []string{"100x100", "300x300"}
