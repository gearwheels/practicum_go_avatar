// Package imaging отвечает за декодирование исходных изображений и
// генерацию миниатюр. Используется воркером (см. internal/worker) — сама
// генерация миниатюр по ТЗ выполняется асинхронно, а не в HTTP-хендлере.
package imaging

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // регистрирует декодер image/webp для image.Decode
)

// Spec описывает один требуемый размер миниатюры.
type Spec struct {
	Label  string // "100x100"
	Width  int
	Height int
}

// Specs — размеры миниатюр из ТЗ.
var Specs = []Spec{
	{Label: "100x100", Width: 100, Height: 100},
	{Label: "300x300", Width: 300, Height: 300},
}

// Decode декодирует изображение произвольного из поддерживаемых форматов
// (jpeg, png, webp — webp только на чтение, см. техническое задание).
func Decode(r io.Reader) (image.Image, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("декодирование изображения: %w", err)
	}
	return img, nil
}

// Generate строит миниатюры для всех Specs, обрезая изображение по центру
// до нужного соотношения сторон (imaging.Fill — типичное поведение для
// квадратных превью аватарок).
func Generate(src image.Image) map[string]image.Image {
	result := make(map[string]image.Image, len(Specs))
	for _, spec := range Specs {
		result[spec.Label] = imaging.Fill(src, spec.Width, spec.Height, imaging.Center, imaging.Lanczos)
	}
	return result
}

// EncodeMimeType возвращает MIME-тип, которым фактически будет закодирована
// миниатюра для исходного изображения с MIME-типом originalMimeType. PNG
// сохраняет прозрачность и кодируется в PNG, всё остальное (включая WebP,
// который мы не умеем кодировать обратно, см. ТЗ) — в JPEG.
func EncodeMimeType(originalMimeType string) string {
	if originalMimeType == "image/png" {
		return "image/png"
	}
	return "image/jpeg"
}

// Encode кодирует изображение в заданный MIME-тип. WebP на выход не
// поддерживается (см. ТЗ) — для image/webp используется JPEG-кодирование с
// сохранением фактического MIME-типа результата на усмотрение вызывающего
// кода.
func Encode(w io.Writer, img image.Image, mimeType string) error {
	switch mimeType {
	case "image/png":
		return png.Encode(w, img)
	default:
		return jpeg.Encode(w, img, &jpeg.Options{Quality: 90})
	}
}
