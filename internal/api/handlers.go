package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"time"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/repository"
	"go-avatar-service/internal/resilience"
	"go-avatar-service/internal/services/avatar"
	"go-avatar-service/internal/webui"
)

// maxUploadSize — ограничение размера файла аватарки из ТЗ (10MB).
const maxUploadSize = 10 << 20

// Сообщения, которые видит клиент при сбоях. Текст оригинальной ошибки в
// ответ не попадает никогда: в нём могут оказаться имя таблицы, структура
// запроса, адрес зависимости или подстрока DSN. Для диагностики ошибка
// нужна в логе сервера — там она и остаётся, вместе с trace_id запроса.
const (
	msgInternalError   = "Internal server error"
	msgUnavailable     = "Service temporarily unavailable"
	msgBadRequest      = "Invalid request"
	msgMalformedUpload = "Malformed multipart request"
	msgDependencyDown  = "dependency check failed"
	msgBrokerDown      = "broker connection lost"
)

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
		return UploadAvatar400JSONResponse{Error: msgBadRequest, Details: badUpload(ctx, "UploadAvatar", err)}, nil
	case file == nil:
		return UploadAvatar400JSONResponse{Error: "Invalid file format", Details: strPtr("file part is required")}, nil
	case !allowedMimeTypes[file.MimeType]:
		return UploadAvatar400JSONResponse{Error: "Invalid file format", Details: strPtr("Supported formats: jpeg, png, webp")}, nil
	}

	a, err := s.avatars.Upload(ctx, request.Params.XUserID, file.FileName, file.MimeType, int64(len(file.Data)), bytes.NewReader(file.Data))
	if err != nil {
		if errors.Is(err, resilience.ErrCircuitOpen) {
			return UploadAvatar503JSONResponse{unavailable(ctx, "UploadAvatar", err)}, nil
		}
		return UploadAvatar500JSONResponse{internalError(ctx, "UploadAvatar", err)}, nil
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
		if errors.Is(err, resilience.ErrCircuitOpen) {
			return GetAvatarById503JSONResponse{unavailable(ctx, "GetAvatarById", err)}, nil
		}
		return GetAvatarById500JSONResponse{internalError(ctx, "GetAvatarById", err)}, nil
	}

	return imageResponseByContentType(result), nil
}

func (s *AvatarServer) GetAvatarMetadata(ctx context.Context, request GetAvatarMetadataRequestObject) (GetAvatarMetadataResponseObject, error) {
	a, err := s.avatars.GetMetadata(ctx, request.AvatarId)
	if errors.Is(err, repository.ErrNotFound) {
		return GetAvatarMetadata404JSONResponse{NotFoundJSONResponse{Error: "Avatar not found"}}, nil
	}
	if err != nil {
		if errors.Is(err, resilience.ErrCircuitOpen) {
			return GetAvatarMetadata503JSONResponse{unavailable(ctx, "GetAvatarMetadata", err)}, nil
		}
		return GetAvatarMetadata500JSONResponse{internalError(ctx, "GetAvatarMetadata", err)}, nil
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
		if errors.Is(err, resilience.ErrCircuitOpen) {
			return GetUserAvatar503JSONResponse{unavailable(ctx, "GetUserAvatar", err)}, nil
		}
		return GetUserAvatar500JSONResponse{internalError(ctx, "GetUserAvatar", err)}, nil
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
		if errors.Is(err, resilience.ErrCircuitOpen) {
			return ListUserAvatars503JSONResponse{unavailable(ctx, "ListUserAvatars", err)}, nil
		}
		return ListUserAvatars500JSONResponse{internalError(ctx, "ListUserAvatars", err)}, nil
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
	case errors.Is(err, resilience.ErrCircuitOpen):
		return DeleteAvatarById503JSONResponse{unavailable(ctx, "DeleteAvatarById", err)}, nil
	default:
		return DeleteAvatarById500JSONResponse{internalError(ctx, "DeleteAvatarById", err)}, nil
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
	case errors.Is(err, resilience.ErrCircuitOpen):
		return DeleteUserAvatar503JSONResponse{unavailable(ctx, "DeleteUserAvatar", err)}, nil
	default:
		return DeleteUserAvatar500JSONResponse{internalError(ctx, "DeleteUserAvatar", err)}, nil
	}
}

// ---------- Здоровье сервиса ----------

func (s *AvatarServer) HealthCheck(ctx context.Context, request HealthCheckRequestObject) (HealthCheckResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	dbStatus := pingComponent(ctx, "database", s.dbPinger)
	storageStatus := pingComponent(ctx, "s3", s.storagePinger)
	brokerStatus := pingComponent(ctx, "broker", s.brokerPinger)

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

// LivenessCheck отвечает на /livez — пробу жизнеспособности для Kubernetes.
//
// В отличие от HealthCheck здесь намеренно НЕТ обращений к БД, S3 и брокеру
// по сети: /health отдаёт 503 при недоступности любой зависимости, и если бы
// его использовали в livenessProbe, kubelet перезапускал бы разом все реплики
// во время кратковременной недоступности Postgres — то есть добивал бы
// систему, которой и так плохо.
//
// Проверяется только невосстановимое состояние процесса: соединение с
// брокером устанавливается один раз при старте и не переподключается,
// поэтому закрытое соединение означает, что под уже не сможет публиковать
// события и его нужно перезапустить. brokerPinger.Ping для AMQP — это
// проверка флага IsClosed(), без сетевого вызова.
func (s *AvatarServer) LivenessCheck(ctx context.Context, request LivenessCheckRequestObject) (LivenessCheckResponseObject, error) {
	if s.brokerPinger != nil {
		if err := s.brokerPinger.Ping(ctx); err != nil {
			slog.ErrorContext(ctx, "проба жизнеспособности не прошла", "component", "broker", "error", err)
			return LivenessCheck503JSONResponse{Status: LivenessStatusStatusDown, Reason: strPtr(msgBrokerDown)}, nil
		}
	}
	return LivenessCheck200JSONResponse{Status: LivenessStatusStatusOk}, nil
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
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: msgBadRequest, Details: badUpload(ctx, "PostUploadForm", err)}}, nil
	case file == nil:
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "Invalid file format", Details: strPtr("file part is required")}}, nil
	case userID == "":
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "user_id is required"}}, nil
	case !allowedMimeTypes[file.MimeType]:
		return PostUploadForm400JSONResponse{BadRequestJSONResponse{Error: "Invalid file format", Details: strPtr("Supported formats: jpeg, png, webp")}}, nil
	}

	html, err := s.web.HandleUploadForm(ctx, userID, file.FileName, file.MimeType, int64(len(file.Data)), bytes.NewReader(file.Data))
	if err != nil {
		// Сбой при сохранении — не ошибка клиента, поэтому не 400: ошибка
		// уходит в общий обработчик Echo, который вернёт 500, а при
		// разомкнутом circuit breaker — 503.
		return nil, err
	}

	return PostUploadForm200TexthtmlResponse{Body: bytes.NewReader(html), ContentLength: int64(len(html))}, nil
}

// ---------- Вспомогательные функции ----------

func strPtr(s string) *string { return &s }

// internalError формирует тело ответа 500: клиенту — только факт сбоя,
// настоящая причина — в лог. Логируется контекстным методом, чтобы в записи
// оказался trace_id и её можно было связать с трейсом (см.
// observability.NewLogger).
func internalError(ctx context.Context, op string, err error) InternalErrorJSONResponse {
	slog.ErrorContext(ctx, "внутренняя ошибка", "op", op, "error", err)
	return InternalErrorJSONResponse{Error: msgInternalError}
}

// unavailable формирует тело ответа 503 для разомкнутого circuit breaker.
// Деталей в ответе нет: из текста ошибки брейкера («postgres: circuit
// breaker is open») клиент узнал бы имя отказавшей зависимости и то, как
// она защищена. Уровень warn, а не error: это ожидаемое временное
// состояние, на которое сервис отвечает штатно.
func unavailable(ctx context.Context, op string, err error) ServiceUnavailableJSONResponse {
	slog.WarnContext(ctx, "зависимость недоступна", "op", op, "error", err)
	return ServiceUnavailableJSONResponse{Error: msgUnavailable}
}

// badUpload формирует тело ответа 400 при неразобранном multipart-запросе.
// Ошибка парсера описывает внутренности обработки запроса, поэтому клиенту
// уходит постоянный текст.
func badUpload(ctx context.Context, op string, err error) *string {
	slog.WarnContext(ctx, "не удалось разобрать multipart-запрос", "op", op, "error", err)
	return strPtr(msgMalformedUpload)
}

// pingComponent проверяет зависимость для /health. Имя нужно логу: в ответе
// причины отказа нет, и без имени в логе было бы не понять, что именно
// отвалилось.
func pingComponent(ctx context.Context, name string, checker Pinger) *ComponentStatus {
	if checker == nil {
		return nil
	}

	start := time.Now()
	err := checker.Ping(ctx)
	latencyMs := int(time.Since(start) / time.Millisecond)

	if err != nil {
		// /health доступен снаружи через Ingress, поэтому текст ошибки
		// драйвера (адрес и порт зависимости, имя бакета) в ответ не идёт.
		slog.WarnContext(ctx, "проверка зависимости не прошла", "component", name, "error", err)
		return &ComponentStatus{Status: ComponentStatusStatusError, LatencyMs: &latencyMs, Error: strPtr(msgDependencyDown)}
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
