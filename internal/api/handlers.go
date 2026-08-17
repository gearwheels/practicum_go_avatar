package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
	"go-avatar-service/internal/services/avatar"
	"go-avatar-service/internal/webui"
)

// maxUploadSize — ограничение размера файла аватарки из ТЗ (10MB).
const maxUploadSize = 10 << 20

// allowedMimeTypes — форматы, которые сервис принимает на загрузку.
// Реальный тип определяется по magic bytes (http.DetectContentType), а не
// по заголовку Content-Type части multipart-запроса, который клиент может
// подделать.
var allowedMimeTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// Pinger — зависимость, доступность которой проверяется в HealthCheck.
type Pinger interface {
	Ping(ctx context.Context) error
}

// AvatarServer implements StrictServerInterface. Это тонкий адаптер:
// разбирает запрос, вызывает сервисный слой, маппит результат в один из
// сгенерированных ...Response типов. Вся бизнес-логика — в
// internal/services/avatar и internal/webui.
type AvatarServer struct {
	avatars *avatar.Service
	web     *webui.Handlers

	dbPinger      Pinger
	storagePinger Pinger
	brokerPinger  Pinger
}

// NewAvatarServer создаёт адаптер HTTP-слоя. Любой из *Pinger может быть
// nil — соответствующий компонент будет просто пропущен в HealthCheck.
func NewAvatarServer(avatars *avatar.Service, web *webui.Handlers, dbPinger, storagePinger, brokerPinger Pinger) *AvatarServer {
	return &AvatarServer{
		avatars:       avatars,
		web:           web,
		dbPinger:      dbPinger,
		storagePinger: storagePinger,
		brokerPinger:  brokerPinger,
	}
}

// ---------- Загрузка ----------

func (s *AvatarServer) UploadAvatar(ctx context.Context, request UploadAvatarRequestObject) (UploadAvatarResponseObject, error) {
	if request.Body == nil {
		return UploadAvatar400JSONResponse{Error: "multipart body is required"}, nil
	}

	file, _, err := parseMultipartUpload(request.Body)
	switch {
	case errors.Is(err, errFileTooLarge):
		return UploadAvatar413JSONResponse{Error: "File too large", MaxSize: maxUploadSize}, nil
	case err != nil:
		return UploadAvatar400JSONResponse{Error: "Invalid request", Details: strPtr(err.Error())}, nil
	case file == nil:
		return UploadAvatar400JSONResponse{Error: "Invalid file format", Details: strPtr("file part is required")}, nil
	case !allowedMimeTypes[file.MimeType]:
		return UploadAvatar400JSONResponse{Error: "Invalid file format", Details: strPtr("Supported formats: jpeg, png, webp")}, nil
	}

	a, err := s.avatars.Upload(ctx, request.Params.XUserID, file.FileName, file.MimeType, int64(len(file.Data)), bytes.NewReader(file.Data))
	if err != nil {
		return UploadAvatar500JSONResponse{InternalErrorJSONResponse{Error: "Internal server error", Details: strPtr(err.Error())}}, nil
	}

	return UploadAvatar201JSONResponse{
		Id:        a.ID,
		UserId:    a.UserID,
		Url:       fmt.Sprintf("/api/v1/avatars/%s", a.ID),
		Status:    AvatarUploadResponseStatusProcessing,
		CreatedAt: a.CreatedAt,
	}, nil
}

// ---------- Получение и метаданные ----------

func (s *AvatarServer) GetAvatarById(ctx context.Context, request GetAvatarByIdRequestObject) (GetAvatarByIdResponseObject, error) {
	size := "original"
	if request.Params.Size != nil {
		size = string(*request.Params.Size)
	}

	result, err := s.avatars.GetImage(ctx, request.AvatarId, size)
	if errors.Is(err, repository.ErrNotFound) {
		return GetAvatarById404JSONResponse{NotFoundJSONResponse{Error: "Avatar not found"}}, nil
	}
	if err != nil {
		return GetAvatarById500JSONResponse{InternalErrorJSONResponse{Error: "Internal server error", Details: strPtr(err.Error())}}, nil
	}

	return imageResponseByContentType(result), nil
}

func (s *AvatarServer) GetAvatarMetadata(ctx context.Context, request GetAvatarMetadataRequestObject) (GetAvatarMetadataResponseObject, error) {
	a, err := s.avatars.GetMetadata(ctx, request.AvatarId)
	if errors.Is(err, repository.ErrNotFound) {
		return GetAvatarMetadata404JSONResponse{NotFoundJSONResponse{Error: "Avatar not found"}}, nil
	}
	if err != nil {
		return GetAvatarMetadata500JSONResponse{InternalErrorJSONResponse{Error: "Internal server error", Details: strPtr(err.Error())}}, nil
	}

	metadata := GetAvatarMetadata200JSONResponse(toAPIMetadata(a))
	return metadata, nil
}

func (s *AvatarServer) GetUserAvatar(ctx context.Context, request GetUserAvatarRequestObject) (GetUserAvatarResponseObject, error) {
	size := "original"
	if request.Params.Size != nil {
		size = string(*request.Params.Size)
	}

	result, err := s.avatars.GetImageForUser(ctx, request.UserId, size)
	if errors.Is(err, repository.ErrNotFound) {
		// У пользователя ещё нет аватарки — отдаём заглушку вместо 404.
		placeholder := webui.Placeholder()
		return GetUserAvatar200ImagepngResponse{
			Body:          bytes.NewReader(placeholder),
			ContentLength: int64(len(placeholder)),
		}, nil
	}
	if err != nil {
		return GetUserAvatar500JSONResponse{InternalErrorJSONResponse{Error: "Internal server error", Details: strPtr(err.Error())}}, nil
	}

	switch result.ContentType {
	case "image/png":
		return GetUserAvatar200ImagepngResponse{Body: result.Body, ContentLength: result.ContentLength, Headers: GetUserAvatar200ResponseHeaders{CacheControl: strPtr("public, max-age=86400"), ETag: strPtr(result.ETag)}}, nil
	case "image/webp":
		return GetUserAvatar200ImagewebpResponse{Body: result.Body, ContentLength: result.ContentLength, Headers: GetUserAvatar200ResponseHeaders{CacheControl: strPtr("public, max-age=86400"), ETag: strPtr(result.ETag)}}, nil
	default:
		return GetUserAvatar200ImagejpegResponse{Body: result.Body, ContentLength: result.ContentLength, Headers: GetUserAvatar200ResponseHeaders{CacheControl: strPtr("public, max-age=86400"), ETag: strPtr(result.ETag)}}, nil
	}
}

func (s *AvatarServer) ListUserAvatars(ctx context.Context, request ListUserAvatarsRequestObject) (ListUserAvatarsResponseObject, error) {
	limit, offset := 20, 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	if request.Params.Offset != nil {
		offset = *request.Params.Offset
	}

	avatars, total, err := s.avatars.ListForUser(ctx, request.UserId, limit, offset)
	if err != nil {
		return ListUserAvatars500JSONResponse{InternalErrorJSONResponse{Error: "Internal server error", Details: strPtr(err.Error())}}, nil
	}

	items := make([]AvatarMetadata, 0, len(avatars))
	for _, a := range avatars {
		items = append(items, toAPIMetadata(a))
	}

	return ListUserAvatars200JSONResponse{
		Items:  &items,
		Limit:  &limit,
		Offset: &offset,
		Total:  &total,
	}, nil
}

// ---------- Удаление ----------

func (s *AvatarServer) DeleteAvatarById(ctx context.Context, request DeleteAvatarByIdRequestObject) (DeleteAvatarByIdResponseObject, error) {
	err := s.avatars.DeleteByID(ctx, request.AvatarId, request.Params.XUserID)
	switch {
	case err == nil:
		return DeleteAvatarById204Response{}, nil
	case errors.Is(err, avatar.ErrForbidden):
		return DeleteAvatarById403JSONResponse{Error: "Forbidden", Details: strPtr("You can only delete your own avatars")}, nil
	case errors.Is(err, repository.ErrNotFound):
		return DeleteAvatarById404JSONResponse{NotFoundJSONResponse{Error: "Avatar not found"}}, nil
	default:
		return DeleteAvatarById500JSONResponse{InternalErrorJSONResponse{Error: "Internal server error", Details: strPtr(err.Error())}}, nil
	}
}

func (s *AvatarServer) DeleteUserAvatar(ctx context.Context, request DeleteUserAvatarRequestObject) (DeleteUserAvatarResponseObject, error) {
	err := s.avatars.DeleteCurrentForUser(ctx, request.UserId, request.Params.XUserID)
	switch {
	case err == nil:
		return DeleteUserAvatar204Response{}, nil
	case errors.Is(err, avatar.ErrForbidden):
		return DeleteUserAvatar403JSONResponse{Error: "Forbidden", Details: strPtr("You can only delete your own avatars")}, nil
	case errors.Is(err, repository.ErrNotFound):
		return DeleteUserAvatar404JSONResponse{NotFoundJSONResponse{Error: "Avatar not found"}}, nil
	default:
		return DeleteUserAvatar500JSONResponse{InternalErrorJSONResponse{Error: "Internal server error", Details: strPtr(err.Error())}}, nil
	}
}

// ---------- Здоровье сервиса ----------

func (s *AvatarServer) HealthCheck(ctx context.Context, request HealthCheckRequestObject) (HealthCheckResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	dbStatus := pingComponent(ctx, s.dbPinger)
	storageStatus := pingComponent(ctx, s.storagePinger)
	brokerStatus := pingComponent(ctx, s.brokerPinger)

	status := HealthStatus{Status: HealthStatusStatusOk}
	status.Components.Database = dbStatus
	status.Components.S3 = storageStatus
	status.Components.Broker = brokerStatus

	for _, c := range []*ComponentStatus{dbStatus, storageStatus, brokerStatus} {
		if c != nil && c.Status == ComponentStatusStatusError {
			status.Status = HealthStatusStatusDown
		}
	}

	if status.Status == HealthStatusStatusDown {
		return HealthCheck503JSONResponse(status), nil
	}
	return HealthCheck200JSONResponse(status), nil
}

// ---------- Веб-интерфейс ----------

func (s *AvatarServer) GetUploadPage(ctx context.Context, request GetUploadPageRequestObject) (GetUploadPageResponseObject, error) {
	html, err := s.web.UploadPage()
	if err != nil {
		return nil, err
	}
	return GetUploadPage200TexthtmlResponse{Body: bytes.NewReader(html), ContentLength: int64(len(html))}, nil
}

func (s *AvatarServer) GetGalleryPage(ctx context.Context, request GetGalleryPageRequestObject) (GetGalleryPageResponseObject, error) {
	html, err := s.web.Gallery(ctx, request.UserId)
	if err != nil {
		return nil, err
	}
	return GetGalleryPage200TexthtmlResponse{Body: bytes.NewReader(html), ContentLength: int64(len(html))}, nil
}

func (s *AvatarServer) PostUploadForm(ctx context.Context, request PostUploadFormRequestObject) (PostUploadFormResponseObject, error) {
	if request.Body == nil {
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "multipart body is required"}}, nil
	}

	file, userID, err := parseMultipartUpload(request.Body)
	switch {
	case err != nil:
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "Invalid request", Details: strPtr(err.Error())}}, nil
	case file == nil:
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "Invalid file format", Details: strPtr("file part is required")}}, nil
	case userID == "":
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "user_id is required"}}, nil
	case !allowedMimeTypes[file.MimeType]:
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "Invalid file format", Details: strPtr("Supported formats: jpeg, png, webp")}}, nil
	}

	html, err := s.web.HandleUploadForm(ctx, userID, file.FileName, file.MimeType, int64(len(file.Data)), bytes.NewReader(file.Data))
	if err != nil {
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "Upload failed", Details: strPtr(err.Error())}}, nil
	}

	return PostUploadForm200TexthtmlResponse{Body: bytes.NewReader(html), ContentLength: int64(len(html))}, nil
}

// ---------- Вспомогательные функции ----------

func strPtr(s string) *string { return &s }

func pingComponent(ctx context.Context, checker Pinger) *ComponentStatus {
	if checker == nil {
		return nil
	}

	start := time.Now()
	err := checker.Ping(ctx)
	latencyMs := int(time.Since(start) / time.Millisecond)

	if err != nil {
		errMsg := err.Error()
		return &ComponentStatus{Status: ComponentStatusStatusError, LatencyMs: &latencyMs, Error: &errMsg}
	}
	return &ComponentStatus{Status: ComponentStatusStatusOk, LatencyMs: &latencyMs}
}

func toAPIMetadata(a domain.Avatar) AvatarMetadata {
	uploadStatus := AvatarMetadataUploadStatus(a.UploadStatus)
	processingStatus := AvatarMetadataProcessingStatus(a.ProcessingStatus)

	var thumbnails []ThumbnailInfo
	for size, key := range a.ThumbnailS3Keys {
		_ = key
		thumbnails = append(thumbnails, ThumbnailInfo{
			Size: size,
			Url:  fmt.Sprintf("/api/v1/avatars/%s?size=%s", a.ID, size),
		})
	}
	var thumbnailsPtr *[]ThumbnailInfo
	if len(thumbnails) > 0 {
		thumbnailsPtr = &thumbnails
	}

	return AvatarMetadata{
		Id:               a.ID,
		UserId:           a.UserID,
		FileName:         a.FileName,
		MimeType:         a.MimeType,
		Size:             a.SizeBytes,
		UploadStatus:     &uploadStatus,
		ProcessingStatus: &processingStatus,
		Thumbnails:       thumbnailsPtr,
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
	}
}

func imageResponseByContentType(result avatar.ImageResult) GetAvatarByIdResponseObject {
	headers := GetAvatarById200ResponseHeaders{
		CacheControl: strPtr("public, max-age=86400"),
		ETag:         strPtr(result.ETag),
	}
	switch result.ContentType {
	case "image/png":
		return GetAvatarById200ImagepngResponse{Body: result.Body, ContentLength: result.ContentLength, Headers: headers}
	case "image/webp":
		return GetAvatarById200ImagewebpResponse{Body: result.Body, ContentLength: result.ContentLength, Headers: headers}
	default:
		return GetAvatarById200ImagejpegResponse{Body: result.Body, ContentLength: result.ContentLength, Headers: headers}
	}
}

// ---------- Разбор multipart-запроса на загрузку ----------

var errFileTooLarge = errors.New("file too large")

// uploadedFile — распакованная часть multipart-запроса с файлом.
type uploadedFile struct {
	FileName string
	MimeType string
	Data     []byte
}

// parseMultipartUpload читает части multipart-запроса, извлекая файл (поле
// "file") и, если есть, идентификатор пользователя (поле "user_id" —
// используется только PostUploadForm). MIME-тип определяется по
// содержимому файла (magic bytes), а не по заголовку части.
func parseMultipartUpload(mr *multipart.Reader) (*uploadedFile, string, error) {
	var (
		file   *uploadedFile
		userID string
	)

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("чтение multipart-запроса: %w", err)
		}

		switch part.FormName() {
		case "file":
			data, err := io.ReadAll(io.LimitReader(part, maxUploadSize+1))
			if err != nil {
				part.Close()
				return nil, "", fmt.Errorf("чтение файла: %w", err)
			}
			if len(data) > maxUploadSize {
				part.Close()
				return nil, "", errFileTooLarge
			}
			file = &uploadedFile{
				FileName: part.FileName(),
				MimeType: http.DetectContentType(data),
				Data:     data,
			}
		case "user_id":
			data, err := io.ReadAll(part)
			if err != nil {
				part.Close()
				return nil, "", fmt.Errorf("чтение user_id: %w", err)
			}
			userID = string(data)
		}
		part.Close()
	}

	return file, userID, nil
}
