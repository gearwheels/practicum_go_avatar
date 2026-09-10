// Package webui рендерит серверные HTML-страницы (форма загрузки, галерея)
// и хранит встроенную заглушку аватарки для пользователей без загруженного
// изображения.
package webui

import (
	"bytes"
	"embed"
	"html/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

//go:embed assets/placeholder.png
var placeholderPNG []byte

// PlaceholderContentType — MIME-тип встроенной заглушки аватарки.
const PlaceholderContentType = "image/png"

// Placeholder возвращает байты изображения-заглушки, отдаваемого вместо 404
// для GetUserAvatar, если у пользователя нет загруженного аватара.
func Placeholder() []byte {
	return placeholderPNG
}

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.tmpl"))

// render рендерит именованный шаблон в байтовый буфер.
func render(name string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
