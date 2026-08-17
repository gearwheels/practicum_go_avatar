package api

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
	"go-avatar-service/internal/services/avatar"
	"go-avatar-service/internal/webui"
)

// ---------- фейки для сборки AvatarServer в тестах ----------

type fakeRepo struct {
	byID map[uuid.UUID]domain.Avatar
}

func newFakeRepo() *fakeRepo { return &fakeRepo{byID: make(map[uuid.UUID]domain.Avatar)} }

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
	for _, a := range r.byID {
		if a.UserID == userID && a.DeletedAt == nil {
			return a, nil
		}
	}
	return domain.Avatar{}, repository.ErrNotFound
}

func (r *fakeRepo) ListByUserID(_ context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error) {
	var all []domain.Avatar
	for _, a := range r.byID {
		if a.UserID == userID && a.DeletedAt == nil {
			all = append(all, a)
		}
	}
	return all, len(all), nil
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

type fakeStorage struct{ objects map[string][]byte }

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

type fakePublisher struct{}

func (fakePublisher) PublishProcessEvent(context.Context, broker.AvatarProcessEvent) error {
	return nil
}
func (fakePublisher) PublishDeleteEvent(context.Context, broker.AvatarDeleteEvent) error { return nil }

type fakePinger struct{ err error }

func (p fakePinger) Ping(context.Context) error { return p.err }

func newTestServer() *AvatarServer {
	repo := newFakeRepo()
	storage := newFakeStorage()
	svc := avatar.NewService(repo, storage, fakePublisher{})
	web := webui.NewHandlers(svc)
	return NewAvatarServer(svc, web, fakePinger{}, fakePinger{}, fakePinger{})
}

func testJPEGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func newMultipartUploadBody(t *testing.T, fields map[string]string, fileField, fileName string, fileData []byte) *multipart.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, w.WriteField(k, v))
	}
	part, err := w.CreateFormFile(fileField, fileName)
	require.NoError(t, err)
	_, err = part.Write(fileData)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return multipart.NewReader(&buf, w.Boundary())
}

// ---------- тесты ----------

func TestUploadAvatar_Success(t *testing.T) {
	s := newTestServer()
	body := newMultipartUploadBody(t, nil, "file", "avatar.jpg", testJPEGBytes(t))

	resp, err := s.UploadAvatar(context.Background(), UploadAvatarRequestObject{
		Params: UploadAvatarParams{XUserID: "user-1"},
		Body:   body,
	})
	require.NoError(t, err)

	created, ok := resp.(UploadAvatar201JSONResponse)
	require.True(t, ok, "ожидался 201, получено %T", resp)
	require.Equal(t, "user-1", created.UserId)
}

func TestUploadAvatar_RejectsUnsupportedFormat(t *testing.T) {
	s := newTestServer()
	body := newMultipartUploadBody(t, nil, "file", "avatar.txt", []byte("не картинка"))

	resp, err := s.UploadAvatar(context.Background(), UploadAvatarRequestObject{
		Params: UploadAvatarParams{XUserID: "user-1"},
		Body:   body,
	})
	require.NoError(t, err)

	_, ok := resp.(UploadAvatar400JSONResponse)
	require.True(t, ok, "ожидался 400, получено %T", resp)
}

func TestGetAvatarMetadata_NotFound(t *testing.T) {
	s := newTestServer()
	resp, err := s.GetAvatarMetadata(context.Background(), GetAvatarMetadataRequestObject{AvatarId: uuid.New()})
	require.NoError(t, err)

	_, ok := resp.(GetAvatarMetadata404JSONResponse)
	require.True(t, ok, "ожидался 404, получено %T", resp)
}

func TestDeleteAvatarById_ForbiddenForOtherUser(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	body := newMultipartUploadBody(t, nil, "file", "avatar.jpg", testJPEGBytes(t))

	uploadResp, err := s.UploadAvatar(ctx, UploadAvatarRequestObject{
		Params: UploadAvatarParams{XUserID: "owner"},
		Body:   body,
	})
	require.NoError(t, err)
	created := uploadResp.(UploadAvatar201JSONResponse)

	resp, err := s.DeleteAvatarById(ctx, DeleteAvatarByIdRequestObject{
		AvatarId: created.Id,
		Params:   DeleteAvatarByIdParams{XUserID: "not-the-owner"},
	})
	require.NoError(t, err)

	_, ok := resp.(DeleteAvatarById403JSONResponse)
	require.True(t, ok, "ожидался 403, получено %T", resp)
}

func TestGetAvatarById_Success(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	body := newMultipartUploadBody(t, nil, "file", "avatar.jpg", testJPEGBytes(t))

	uploadResp, err := s.UploadAvatar(ctx, UploadAvatarRequestObject{
		Params: UploadAvatarParams{XUserID: "user-1"},
		Body:   body,
	})
	require.NoError(t, err)
	created := uploadResp.(UploadAvatar201JSONResponse)

	resp, err := s.GetAvatarById(ctx, GetAvatarByIdRequestObject{AvatarId: created.Id})
	require.NoError(t, err)

	img, ok := resp.(GetAvatarById200ImagejpegResponse)
	require.True(t, ok, "ожидался image/jpeg 200, получено %T", resp)
	require.NotNil(t, img.Headers.ETag)
}

func TestGetAvatarById_NotFound(t *testing.T) {
	s := newTestServer()
	resp, err := s.GetAvatarById(context.Background(), GetAvatarByIdRequestObject{AvatarId: uuid.New()})
	require.NoError(t, err)

	_, ok := resp.(GetAvatarById404JSONResponse)
	require.True(t, ok, "ожидался 404, получено %T", resp)
}

func TestGetUserAvatar_PlaceholderWhenMissing(t *testing.T) {
	s := newTestServer()
	resp, err := s.GetUserAvatar(context.Background(), GetUserAvatarRequestObject{UserId: "no-such-user"})
	require.NoError(t, err)

	img, ok := resp.(GetUserAvatar200ImagepngResponse)
	require.True(t, ok, "ожидалась заглушка image/png, получено %T", resp)
	require.NotZero(t, img.ContentLength)
}

func TestGetUserAvatar_ReturnsUploadedAvatar(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	body := newMultipartUploadBody(t, nil, "file", "avatar.jpg", testJPEGBytes(t))

	_, err := s.UploadAvatar(ctx, UploadAvatarRequestObject{
		Params: UploadAvatarParams{XUserID: "user-1"},
		Body:   body,
	})
	require.NoError(t, err)

	resp, err := s.GetUserAvatar(ctx, GetUserAvatarRequestObject{UserId: "user-1"})
	require.NoError(t, err)

	_, ok := resp.(GetUserAvatar200ImagejpegResponse)
	require.True(t, ok, "ожидался image/jpeg 200, получено %T", resp)
}

func TestListUserAvatars_DefaultsAndTotal(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		body := newMultipartUploadBody(t, nil, "file", "avatar.jpg", testJPEGBytes(t))
		_, err := s.UploadAvatar(ctx, UploadAvatarRequestObject{
			Params: UploadAvatarParams{XUserID: "user-1"},
			Body:   body,
		})
		require.NoError(t, err)
	}

	resp, err := s.ListUserAvatars(ctx, ListUserAvatarsRequestObject{UserId: "user-1"})
	require.NoError(t, err)

	list := resp.(ListUserAvatars200JSONResponse)
	require.Equal(t, 2, *list.Total)
	require.Equal(t, 20, *list.Limit)
	require.Len(t, *list.Items, 2)
}

func TestDeleteUserAvatar_ForbiddenOnHeaderMismatch(t *testing.T) {
	s := newTestServer()
	resp, err := s.DeleteUserAvatar(context.Background(), DeleteUserAvatarRequestObject{
		UserId: "user-1",
		Params: DeleteUserAvatarParams{XUserID: "someone-else"},
	})
	require.NoError(t, err)

	_, ok := resp.(DeleteUserAvatar403JSONResponse)
	require.True(t, ok, "ожидался 403, получено %T", resp)
}

func TestWebPages_UploadGalleryAndPostForm(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	uploadPage, err := s.GetUploadPage(ctx, GetUploadPageRequestObject{})
	require.NoError(t, err)
	require.IsType(t, GetUploadPage200TexthtmlResponse{}, uploadPage)

	galleryPage, err := s.GetGalleryPage(ctx, GetGalleryPageRequestObject{UserId: "user-1"})
	require.NoError(t, err)
	require.IsType(t, GetGalleryPage200TexthtmlResponse{}, galleryPage)

	body := newMultipartUploadBody(t, map[string]string{"user_id": "user-1"}, "file", "avatar.jpg", testJPEGBytes(t))
	postResp, err := s.PostUploadForm(ctx, PostUploadFormRequestObject{Body: body})
	require.NoError(t, err)
	require.IsType(t, PostUploadForm200TexthtmlResponse{}, postResp)
}

func TestPostUploadForm_RejectsMissingUserID(t *testing.T) {
	s := newTestServer()
	body := newMultipartUploadBody(t, nil, "file", "avatar.jpg", testJPEGBytes(t))

	resp, err := s.PostUploadForm(context.Background(), PostUploadFormRequestObject{Body: body})
	require.NoError(t, err)
	require.IsType(t, PostUploadForm400JSONResponse{}, resp)
}

func TestHealthCheck_AllOk(t *testing.T) {
	s := newTestServer()
	resp, err := s.HealthCheck(context.Background(), HealthCheckRequestObject{})
	require.NoError(t, err)

	ok, isOk := resp.(HealthCheck200JSONResponse)
	require.True(t, isOk, "ожидался 200, получено %T", resp)
	require.Equal(t, HealthStatusStatusOk, ok.Status)
}

func TestHealthCheck_DownWhenComponentFails(t *testing.T) {
	repo := newFakeRepo()
	storage := newFakeStorage()
	svc := avatar.NewService(repo, storage, fakePublisher{})
	web := webui.NewHandlers(svc)
	s := NewAvatarServer(svc, web, fakePinger{err: errors.New("нет соединения")}, fakePinger{}, fakePinger{})

	resp, err := s.HealthCheck(context.Background(), HealthCheckRequestObject{})
	require.NoError(t, err)

	down, isDown := resp.(HealthCheck503JSONResponse)
	require.True(t, isDown, "ожидался 503, получено %T", resp)
	require.Equal(t, HealthStatusStatusDown, down.Status)
}
