// Package broker отвечает за асинхронный обмен событиями через RabbitMQ:
// объявление топологии (exchange/очереди/DLQ), публикацию и потребление
// сообщений.
package broker

import "github.com/google/uuid"

// AvatarProcessEvent — событие "нужно сгенерировать миниатюры для аватарки".
// Публикуется синхронно после успешной загрузки оригинала в S3.
type AvatarProcessEvent struct {
	AvatarID uuid.UUID `json:"avatar_id"`
	UserID   string    `json:"user_id"`
	S3Key    string    `json:"s3_key"`
}

// AvatarDeleteEvent — событие "нужно удалить файлы аватарки из S3".
// Публикуется после мягкого удаления записи в БД.
type AvatarDeleteEvent struct {
	AvatarID uuid.UUID `json:"avatar_id"`
	S3Keys   []string  `json:"s3_keys"`
}
