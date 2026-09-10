package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
)

// AvatarRepository — реализация repository.AvatarRepository поверх pgx.
type AvatarRepository struct {
	pool *pgxpool.Pool
}

// NewAvatarRepository создаёт репозиторий аватарок.
func NewAvatarRepository(pool *pgxpool.Pool) *AvatarRepository {
	return &AvatarRepository{pool: pool}
}

// Create вставляет запись и возвращает её с полями, выставленными на
// стороне БД (created_at/updated_at заполняются через DEFAULT NOW()) —
// вызывающий код не должен подставлять время сам и потом гадать, совпадёт
// ли оно с тем, что реально попало в строку.
func (r *AvatarRepository) Create(ctx context.Context, a domain.Avatar) (domain.Avatar, error) {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, s3_key, upload_status, processing_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at
	`, a.ID, a.UserID, a.FileName, a.MimeType, a.SizeBytes, a.S3Key, a.UploadStatus, a.ProcessingStatus,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("вставка аватарки: %w", err)
	}
	return a, nil
}

func (r *AvatarRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	row := r.pool.QueryRow(ctx, selectQuery+` WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanAvatar(row)
}

func (r *AvatarRepository) GetLatestByUserID(ctx context.Context, userID string) (domain.Avatar, error) {
	row := r.pool.QueryRow(ctx, selectQuery+`
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
	`, userID)
	return scanAvatar(row)
}

func (r *AvatarRepository) ListByUserID(ctx context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error) {
	rows, err := r.pool.Query(ctx, selectQuery+`
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("выборка аватарок пользователя: %w", err)
	}
	defer rows.Close()

	var avatars []domain.Avatar
	for rows.Next() {
		a, err := scanAvatar(rows)
		if err != nil {
			return nil, 0, err
		}
		avatars = append(avatars, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM avatars WHERE user_id = $1 AND deleted_at IS NULL
	`, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("подсчёт аватарок пользователя: %w", err)
	}

	return avatars, total, nil
}

func (r *AvatarRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE avatars SET deleted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("мягкое удаление аватарки: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *AvatarRepository) UpdateProcessingStatus(ctx context.Context, id uuid.UUID, status string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE avatars SET processing_status = $2, updated_at = NOW() WHERE id = $1
	`, id, status)
	if err != nil {
		return fmt.Errorf("обновление статуса обработки: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *AvatarRepository) UpdateThumbnails(ctx context.Context, id uuid.UUID, thumbnails map[string]string) error {
	data, err := json.Marshal(thumbnails)
	if err != nil {
		return fmt.Errorf("сериализация ключей миниатюр: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE avatars SET thumbnail_s3_keys = $2, updated_at = NOW() WHERE id = $1
	`, id, data)
	if err != nil {
		return fmt.Errorf("обновление ключей миниатюр: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

const selectQuery = `
	SELECT id, user_id, file_name, mime_type, size_bytes, s3_key, thumbnail_s3_keys,
	       upload_status, processing_status, created_at, updated_at, deleted_at
	FROM avatars
`

// rowScanner объединяет pgx.Row и pgx.Rows — у обоих есть Scan с одинаковой
// сигнатурой, что позволяет переиспользовать scanAvatar для единичной строки
// и для перебора набора строк.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAvatar(row rowScanner) (domain.Avatar, error) {
	var (
		a            domain.Avatar
		thumbnailRaw []byte
	)
	err := row.Scan(
		&a.ID, &a.UserID, &a.FileName, &a.MimeType, &a.SizeBytes, &a.S3Key, &thumbnailRaw,
		&a.UploadStatus, &a.ProcessingStatus, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Avatar{}, repository.ErrNotFound
		}
		return domain.Avatar{}, fmt.Errorf("чтение аватарки: %w", err)
	}
	if len(thumbnailRaw) > 0 {
		if err := json.Unmarshal(thumbnailRaw, &a.ThumbnailS3Keys); err != nil {
			return domain.Avatar{}, fmt.Errorf("разбор ключей миниатюр: %w", err)
		}
	}
	return a, nil
}
