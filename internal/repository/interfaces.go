// Package repository определяет интерфейс доступа к хранилищу метаданных
// аватарок. Конкретная реализация — в подпакете postgres.
package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
)

// ErrNotFound возвращается, когда аватарка не найдена (или мягко удалена).
var ErrNotFound = errors.New("avatar not found")

// AvatarRepository — доступ к метаданным аватарок в БД.
type AvatarRepository interface {
	Create(ctx context.Context, a domain.Avatar) (domain.Avatar, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Avatar, error)
	GetLatestByUserID(ctx context.Context, userID string) (domain.Avatar, error)
	ListByUserID(ctx context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
	UpdateProcessingStatus(ctx context.Context, id uuid.UUID, status string) error
	UpdateThumbnails(ctx context.Context, id uuid.UUID, thumbnails map[string]string) error
}
