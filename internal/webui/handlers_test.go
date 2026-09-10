package webui

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
)

type fakeAvatarService struct {
	uploaded []string
	list     []domain.Avatar
}

func (f *fakeAvatarService) Upload(_ context.Context, userID, _, _ string, _ int64, r io.Reader) (domain.Avatar, error) {
	data, _ := io.ReadAll(r)
	f.uploaded = append(f.uploaded, string(data))
	return domain.Avatar{UserID: userID}, nil
}

func (f *fakeAvatarService) ListForUser(context.Context, string, int, int) ([]domain.Avatar, int, error) {
	return f.list, len(f.list), nil
}

func TestUploadPage_Renders(t *testing.T) {
	h := NewHandlers(&fakeAvatarService{})
	html, err := h.UploadPage()
	require.NoError(t, err)
	require.Contains(t, string(html), `action="/web/upload"`)
}

func TestGallery_EmptyAndWithAvatars(t *testing.T) {
	svc := &fakeAvatarService{}
	h := NewHandlers(svc)

	html, err := h.Gallery(context.Background(), "user-1")
	require.NoError(t, err)
	require.Contains(t, string(html), "нет загруженных аватарок")

	svc.list = []domain.Avatar{{FileName: "avatar.jpg", UploadStatus: "uploaded", ProcessingStatus: "completed"}}
	html, err = h.Gallery(context.Background(), "user-1")
	require.NoError(t, err)
	require.Contains(t, string(html), "avatar.jpg")
}

func TestHandleUploadForm_RendersSuccessPage(t *testing.T) {
	svc := &fakeAvatarService{}
	h := NewHandlers(svc)

	html, err := h.HandleUploadForm(context.Background(), "user-1", "a.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)
	require.Contains(t, string(html), "/web/gallery/user-1")
	require.Equal(t, []string{"hello"}, svc.uploaded)
}

func TestPlaceholder_NotEmpty(t *testing.T) {
	require.NotEmpty(t, Placeholder())
}
