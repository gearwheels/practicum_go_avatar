package webui

import (
	"context"
	"io"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
)

// avatarService — то, что нужно веб-интерфейсу от сервиса аватарок.
type avatarService interface {
	Upload(ctx context.Context, userID, fileName, mimeType string, size int64, r io.Reader) (domain.Avatar, error)
	ListForUser(ctx context.Context, userID string, limit, offset int) ([]domain.Avatar, int, error)
}

// Handlers рендерит серверные HTML-страницы поверх сервиса аватарок.
type Handlers struct {
	avatars avatarService
}

// NewHandlers создаёт обработчики веб-интерфейса.
func NewHandlers(avatars avatarService) *Handlers {
	return &Handlers{avatars: avatars}
}

// UploadPage рендерит статичную форму загрузки.
func (h *Handlers) UploadPage() ([]byte, error) {
	return render("upload.html.tmpl", nil)
}

// galleryData — данные для шаблона gallery.html.tmpl.
type galleryData struct {
	UserID  string
	Avatars []domain.Avatar
}

// Gallery рендерит галерею аватарок пользователя (пустая галерея — тоже
// валидная страница, а не ошибка).
func (h *Handlers) Gallery(ctx context.Context, userID string) ([]byte, error) {
	avatars, _, err := h.avatars.ListForUser(ctx, userID, 100, 0)
	if err != nil && err != repository.ErrNotFound {
		return nil, err
	}
	return render("gallery.html.tmpl", galleryData{UserID: userID, Avatars: avatars})
}

// HandleUploadForm переиспользует ту же бизнес-логику загрузки, что и
// UploadAvatar (см. internal/api/handlers.go), и рендерит страницу успеха
// с переходом в галерею.
func (h *Handlers) HandleUploadForm(ctx context.Context, userID, fileName, mimeType string, size int64, r io.Reader) ([]byte, error) {
	a, err := h.avatars.Upload(ctx, userID, fileName, mimeType, size, r)
	if err != nil {
		return nil, err
	}
	return render("upload_success.html.tmpl", struct{ UserID string }{UserID: a.UserID})
}
