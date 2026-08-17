package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
)

type fakeRepo struct {
	byID map[uuid.UUID]domain.Avatar
}

func newFakeRepo() *fakeRepo { return &fakeRepo{byID: make(map[uuid.UUID]domain.Avatar)} }

func (r *fakeRepo) Create(_ context.Context, a domain.Avatar) (domain.Avatar, error) { return a, nil }

func (r *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Avatar, error) {
	a, ok := r.byID[id]
	if !ok {
		return domain.Avatar{}, repository.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) GetLatestByUserID(context.Context, string) (domain.Avatar, error) {
	return domain.Avatar{}, repository.ErrNotFound
}

func (r *fakeRepo) ListByUserID(context.Context, string, int, int) ([]domain.Avatar, int, error) {
	return nil, 0, nil
}

func (r *fakeRepo) SoftDelete(context.Context, uuid.UUID) error { return nil }

func (r *fakeRepo) UpdateProcessingStatus(_ context.Context, id uuid.UUID, status string) error {
	a := r.byID[id]
	a.ProcessingStatus = status
	r.byID[id] = a
	return nil
}

func (r *fakeRepo) UpdateThumbnails(_ context.Context, id uuid.UUID, thumbnails map[string]string) error {
	a := r.byID[id]
	a.ThumbnailS3Keys = thumbnails
	r.byID[id] = a
	return nil
}

type fakeStorage struct {
	objects       map[string][]byte
	deletedPrefix string
}

func newFakeStorage() *fakeStorage { return &fakeStorage{objects: make(map[string][]byte)} }

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

func (s *fakeStorage) DeletePrefix(_ context.Context, prefix string) error {
	s.deletedPrefix = prefix
	return nil
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func TestHandleProcess_GeneratesThumbnails(t *testing.T) {
	repo := newFakeRepo()
	storage := newFakeStorage()
	id := uuid.New()

	repo.byID[id] = domain.Avatar{
		ID:               id,
		UserID:           "user-1",
		MimeType:         "image/jpeg",
		S3Key:            "avatars/" + id.String() + "/original.jpg",
		ProcessingStatus: domain.ProcessingStatusPending,
	}
	storage.objects[repo.byID[id].S3Key] = testJPEG(t)

	h := New(repo, storage)
	body, err := json.Marshal(broker.AvatarProcessEvent{AvatarID: id, UserID: "user-1", S3Key: repo.byID[id].S3Key})
	require.NoError(t, err)

	require.NoError(t, h.HandleProcess(context.Background(), body))

	updated := repo.byID[id]
	require.Equal(t, domain.ProcessingStatusCompleted, updated.ProcessingStatus)
	require.Len(t, updated.ThumbnailS3Keys, 2)
	for _, key := range updated.ThumbnailS3Keys {
		require.Contains(t, storage.objects, key)
	}
}

func TestHandleProcess_SkipsAlreadyCompleted(t *testing.T) {
	repo := newFakeRepo()
	storage := newFakeStorage()
	id := uuid.New()
	repo.byID[id] = domain.Avatar{ID: id, ProcessingStatus: domain.ProcessingStatusCompleted}

	h := New(repo, storage)
	body, _ := json.Marshal(broker.AvatarProcessEvent{AvatarID: id})

	require.NoError(t, h.HandleProcess(context.Background(), body))
	require.Empty(t, storage.objects, "для уже обработанной аватарки миниатюры генерироваться не должны")
}

func TestHandleProcess_MissingAvatarIsNotAnError(t *testing.T) {
	repo := newFakeRepo()
	storage := newFakeStorage()
	h := New(repo, storage)

	body, _ := json.Marshal(broker.AvatarProcessEvent{AvatarID: uuid.New()})
	require.NoError(t, h.HandleProcess(context.Background(), body))
}

func TestHandleDelete_RemovesPrefix(t *testing.T) {
	repo := newFakeRepo()
	storage := newFakeStorage()
	h := New(repo, storage)
	id := uuid.New()

	body, err := json.Marshal(broker.AvatarDeleteEvent{AvatarID: id, S3Keys: []string{"a", "b"}})
	require.NoError(t, err)

	require.NoError(t, h.HandleDelete(context.Background(), body))
	require.Equal(t, "avatars/"+id.String()+"/", storage.deletedPrefix)
}
