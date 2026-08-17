package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 100, A: 255})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func TestDecode(t *testing.T) {
	data := newTestJPEG(t, 400, 300)

	img, err := Decode(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, 400, img.Bounds().Dx())
	require.Equal(t, 300, img.Bounds().Dy())
}

func TestGenerate(t *testing.T) {
	data := newTestJPEG(t, 400, 300)
	src, err := Decode(bytes.NewReader(data))
	require.NoError(t, err)

	thumbnails := Generate(src)
	require.Len(t, thumbnails, len(Specs))

	for _, spec := range Specs {
		thumb, ok := thumbnails[spec.Label]
		require.True(t, ok, "миниатюра %s должна быть сгенерирована", spec.Label)
		require.Equal(t, spec.Width, thumb.Bounds().Dx())
		require.Equal(t, spec.Height, thumb.Bounds().Dy())
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	data := newTestJPEG(t, 100, 100)
	src, err := Decode(bytes.NewReader(data))
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, Encode(&buf, src, "image/jpeg"))
	require.NotZero(t, buf.Len())

	decoded, err := Decode(&buf)
	require.NoError(t, err)
	require.Equal(t, src.Bounds(), decoded.Bounds())
}

func TestEncodeMimeType(t *testing.T) {
	require.Equal(t, "image/png", EncodeMimeType("image/png"))
	require.Equal(t, "image/jpeg", EncodeMimeType("image/jpeg"))
	require.Equal(t, "image/jpeg", EncodeMimeType("image/webp"))
}
