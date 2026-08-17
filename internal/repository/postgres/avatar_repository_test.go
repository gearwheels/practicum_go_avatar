//go:build integration

// Интеграционные тесты репозитория. Требуют поднятого PostgreSQL —
// проще всего запустить его через docker-compose (`docker compose up -d postgres`)
// и прогнать: go test -tags=integration ./internal/repository/...
package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
)

// testDatabaseURL возвращает DSN для тестовой БД: DATABASE_URL из окружения,
// если задан (например, при запуске в CI), иначе — адрес Postgres,
// поднятого локально через docker-compose (порт 5432 проброшен на хост).
func testDatabaseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://avatar:avatar@localhost:5432/avatar_service?sslmode=disable"
}

// setupTestRepo поднимает пул соединений, прогоняет миграции и возвращает
// репозиторий вместе с функцией очистки.
func setupTestRepo(t *testing.T) (*AvatarRepository, *pgxpool.Pool) {
	t.Helper()

	dsn := testDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Skipf("PostgreSQL недоступен по %s, пропускаем интеграционные тесты: %v", dsn, err)
	}
	t.Cleanup(pool.Close)

	require.NoError(t, RunMigrations(dsn, "../../../migrations"))

	return NewAvatarRepository(pool), pool
}

// createTestAvatar создаёт тестовую аватарку и регистрирует её удаление
// после завершения теста, чтобы тесты не зависели друг от друга.
func createTestAvatar(t *testing.T, repo *AvatarRepository, pool *pgxpool.Pool, userID string) domain.Avatar {
	t.Helper()

	a := domain.Avatar{
		ID:               uuid.New(),
		UserID:           userID,
		FileName:         "avatar.jpg",
		MimeType:         "image/jpeg",
		SizeBytes:        1024,
		S3Key:            "avatars/test/original.jpg",
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}

	created, err := repo.Create(context.Background(), a)
	require.NoError(t, err)
	require.False(t, created.CreatedAt.IsZero(), "created_at должен быть заполнен на стороне БД")

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM avatars WHERE id = $1`, created.ID)
	})

	return created
}

func TestAvatarRepository_CreateAndGetByID(t *testing.T) {
	repo, pool := setupTestRepo(t)
	a := createTestAvatar(t, repo, pool, "user-1")

	fetched, err := repo.GetByID(context.Background(), a.ID)
	require.NoError(t, err)
	require.Equal(t, a.UserID, fetched.UserID)
	require.Equal(t, a.FileName, fetched.FileName)
	require.Equal(t, domain.ProcessingStatusPending, fetched.ProcessingStatus)
}

func TestAvatarRepository_GetByID_NotFound(t *testing.T) {
	repo, _ := setupTestRepo(t)

	_, err := repo.GetByID(context.Background(), uuid.New())
	require.ErrorIs(t, err, repository.ErrNotFound)
}

func TestAvatarRepository_GetLatestByUserID(t *testing.T) {
	repo, pool := setupTestRepo(t)
	userID := "user-" + uuid.New().String()

	first := createTestAvatar(t, repo, pool, userID)
	time.Sleep(10 * time.Millisecond) // гарантируем разный created_at
	second := createTestAvatar(t, repo, pool, userID)

	latest, err := repo.GetLatestByUserID(context.Background(), userID)
	require.NoError(t, err)
	require.Equal(t, second.ID, latest.ID)
	require.NotEqual(t, first.ID, latest.ID)
}

func TestAvatarRepository_ListByUserID(t *testing.T) {
	repo, pool := setupTestRepo(t)
	userID := "user-" + uuid.New().String()

	for i := 0; i < 3; i++ {
		createTestAvatar(t, repo, pool, userID)
	}

	avatars, total, err := repo.ListByUserID(context.Background(), userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, avatars, 3)
}

func TestAvatarRepository_SoftDelete(t *testing.T) {
	repo, pool := setupTestRepo(t)
	a := createTestAvatar(t, repo, pool, "user-1")

	require.NoError(t, repo.SoftDelete(context.Background(), a.ID))

	_, err := repo.GetByID(context.Background(), a.ID)
	require.ErrorIs(t, err, repository.ErrNotFound, "мягко удалённая запись не должна возвращаться GetByID")
}

func TestAvatarRepository_SoftDelete_NotFound(t *testing.T) {
	repo, _ := setupTestRepo(t)

	err := repo.SoftDelete(context.Background(), uuid.New())
	require.ErrorIs(t, err, repository.ErrNotFound)
}

func TestAvatarRepository_UpdateProcessingStatus(t *testing.T) {
	repo, pool := setupTestRepo(t)
	a := createTestAvatar(t, repo, pool, "user-1")

	require.NoError(t, repo.UpdateProcessingStatus(context.Background(), a.ID, domain.ProcessingStatusCompleted))

	fetched, err := repo.GetByID(context.Background(), a.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProcessingStatusCompleted, fetched.ProcessingStatus)
}

func TestAvatarRepository_UpdateThumbnails(t *testing.T) {
	repo, pool := setupTestRepo(t)
	a := createTestAvatar(t, repo, pool, "user-1")

	thumbs := map[string]string{
		"100x100": "avatars/test/thumb_100x100.jpg",
		"300x300": "avatars/test/thumb_300x300.jpg",
	}
	require.NoError(t, repo.UpdateThumbnails(context.Background(), a.ID, thumbs))

	fetched, err := repo.GetByID(context.Background(), a.ID)
	require.NoError(t, err)
	require.Equal(t, thumbs, fetched.ThumbnailS3Keys)
}
