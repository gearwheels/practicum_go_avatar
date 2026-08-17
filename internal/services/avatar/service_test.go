package avatar

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
)

// fakeRepo — реализация repository.AvatarRepository в памяти для тестов.
type fakeRepo struct {
	byID map[uuid.UUID]domain.Avatar
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: make(map[uuid.UUID]domain.Avatar)}
}

func (r *fakeRepo) Create(_ context.Context, a domain.Avatar) (domain.Avatar, error) {
	a.CreatedAt = time.Now()
	a.UpdatedAt = a.CreatedAt
	r.byID[a.ID] = a
	return a, nil
}

func (r *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Avatar, error) {
	a, ok := r.byID[id]
	if !ok || a.DeletedAt != nil {
		return domain.Avatar{}, repository.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) GetLatestByUserID(_ context.Context, userID string) (domain.Avatar, error) {
	var latest domain.Avatar
	found := false
	for _, a := range r.byID {
		if a.UserID != userID || a.DeletedAt != nil {
			continue
		}
		if !found || a.CreatedAt.After(latest.CreatedAt) {
			latest = a
			found = true
		}
	}
	if !found {
		return domain.Avatar{}, repository.ErrNotFound
	}
	return latest, nil
}

func (r *fakeRepo) ListByUserID(_ context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error) {
	var all []domain.Avatar
	for _, a := range r.byID {
		if a.UserID == userID && a.DeletedAt == nil {
			all = append(all, a)
		}
	}
	total := len(all)
	if offset > len(all) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

func (r *fakeRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	a, ok := r.byID[id]
	if !ok {
		return repository.ErrNotFound
	}
	now := a.UpdatedAt
	a.DeletedAt = &now
	r.byID[id] = a
	return nil
}

func (r *fakeRepo) UpdateProcessingStatus(_ context.Context, id uuid.UUID, status string) error {
	a, ok := r.byID[id]
	if !ok {
		return repository.ErrNotFound
	}
	a.ProcessingStatus = status
	r.byID[id] = a
	return nil
}

func (r *fakeRepo) UpdateThumbnails(_ context.Context, id uuid.UUID, thumbnails map[string]string) error {
	a, ok := r.byID[id]
	if !ok {
		return repository.ErrNotFound
	}
	a.ThumbnailS3Keys = thumbnails
	r.byID[id] = a
	return nil
}

// fakeStorage — реализация Storage в памяти.
type fakeStorage struct {
	objects map[string][]byte
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: make(map[string][]byte)}
}

func (s *fakeStorage) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.objects[key] = data
	return nil
}

func (s *fakeStorage) Download(_ context.Context, key string) (io.ReadCloser, int64, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, 0, repository.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

// fakePublisher — реализация EventPublisher, запоминающая опубликованные события.
type fakePublisher struct {
	processEvents []broker.AvatarProcessEvent
	deleteEvents  []broker.AvatarDeleteEvent
}

func (p *fakePublisher) PublishProcessEvent(_ context.Context, ev broker.AvatarProcessEvent) error {
	p.processEvents = append(p.processEvents, ev)
	return nil
}

func (p *fakePublisher) PublishDeleteEvent(_ context.Context, ev broker.AvatarDeleteEvent) error {
	p.deleteEvents = append(p.deleteEvents, ev)
	return nil
}

func newTestService() (*Service, *fakeRepo, *fakeStorage, *fakePublisher) {
	repo := newFakeRepo()
	storage := newFakeStorage()
	publisher := &fakePublisher{}
	return NewService(repo, storage, publisher), repo, storage, publisher
}

func TestService_Upload(t *testing.T) {
	svc, repo, storage, publisher := newTestService()
	ctx := context.Background()

	a, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)
	require.Equal(t, "user-1", a.UserID)
	require.Equal(t, domain.UploadStatusUploaded, a.UploadStatus)
	require.Equal(t, domain.ProcessingStatusPending, a.ProcessingStatus)

	stored, ok := repo.byID[a.ID]
	require.True(t, ok)
	require.Equal(t, a, stored)

	require.Contains(t, storage.objects, a.S3Key)
	require.Equal(t, []byte("hello"), storage.objects[a.S3Key])

	require.Len(t, publisher.processEvents, 1)
	require.Equal(t, a.ID, publisher.processEvents[0].AvatarID)
}

func TestService_GetImage_FallsBackToOriginalWhenThumbnailMissing(t *testing.T) {
	svc, _, _, _ := newTestService()
	ctx := context.Background()

	a, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)

	result, err := svc.GetImage(ctx, a.ID, "100x100")
	require.NoError(t, err)
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), data)
}

func TestService_GetImage_NotFound(t *testing.T) {
	svc, _, _, _ := newTestService()
	_, err := svc.GetImage(context.Background(), uuid.New(), "original")
	require.ErrorIs(t, err, repository.ErrNotFound)
}

func TestService_DeleteByID_Forbidden(t *testing.T) {
	svc, _, _, _ := newTestService()
	ctx := context.Background()

	a, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)

	err = svc.DeleteByID(ctx, a.ID, "someone-else")
	require.ErrorIs(t, err, ErrForbidden)
}

func TestService_DeleteByID_Success(t *testing.T) {
	svc, repo, _, publisher := newTestService()
	ctx := context.Background()

	a, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)

	require.NoError(t, svc.DeleteByID(ctx, a.ID, "user-1"))

	_, err = repo.GetByID(ctx, a.ID)
	require.ErrorIs(t, err, repository.ErrNotFound)

	require.Len(t, publisher.deleteEvents, 1)
	require.Equal(t, a.ID, publisher.deleteEvents[0].AvatarID)
}

func TestService_DeleteCurrentForUser_ForbiddenOnMismatch(t *testing.T) {
	svc, _, _, _ := newTestService()
	err := svc.DeleteCurrentForUser(context.Background(), "user-1", "user-2")
	require.ErrorIs(t, err, ErrForbidden)
}

func TestService_ListForUser(t *testing.T) {
	svc, _, _, _ := newTestService()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
		require.NoError(t, err)
	}

	avatars, total, err := svc.ListForUser(ctx, "user-1", 20, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, avatars, 3)
}
