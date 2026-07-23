package api

import "context"

// AvatarServer implements StrictServerInterface. Each method below is a stub;
// fill in the real logic (calls into your service/storage layer) as it's built.
type AvatarServer struct{}

func NewAvatarServer() *AvatarServer {
	return &AvatarServer{}
}

func (s *AvatarServer) UploadAvatar(ctx context.Context, request UploadAvatarRequestObject) (UploadAvatarResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) DeleteAvatarById(ctx context.Context, request DeleteAvatarByIdRequestObject) (DeleteAvatarByIdResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) GetAvatarById(ctx context.Context, request GetAvatarByIdRequestObject) (GetAvatarByIdResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) GetAvatarMetadata(ctx context.Context, request GetAvatarMetadataRequestObject) (GetAvatarMetadataResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) DeleteUserAvatar(ctx context.Context, request DeleteUserAvatarRequestObject) (DeleteUserAvatarResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) GetUserAvatar(ctx context.Context, request GetUserAvatarRequestObject) (GetUserAvatarResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) ListUserAvatars(ctx context.Context, request ListUserAvatarsRequestObject) (ListUserAvatarsResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) HealthCheck(ctx context.Context, request HealthCheckRequestObject) (HealthCheckResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) GetGalleryPage(ctx context.Context, request GetGalleryPageRequestObject) (GetGalleryPageResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) GetUploadPage(ctx context.Context, request GetUploadPageRequestObject) (GetUploadPageResponseObject, error) {
	panic("not implemented")
}

func (s *AvatarServer) PostUploadForm(ctx context.Context, request PostUploadFormRequestObject) (PostUploadFormResponseObject, error) {
	panic("not implemented")
}
