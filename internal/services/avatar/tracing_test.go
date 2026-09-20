package avatar

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans подменяет глобальный TracerProvider на записывающий и
// возвращает рекордер завершённых спанов.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return recorder
}

// spanByName ищет завершённый спан по имени.
func spanByName(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range recorder.Ended() {
		if s.Name() == name {
			return s
		}
	}
	require.Failf(t, "спан не найден", "нет завершённого спана %q", name)
	return nil
}

// Внутренние шаги открывают собственные дочерние спаны через ctx, а не
// получают спан вызывающего кода параметром: в трейсе скачивание — отдельный
// шаг внутри get_avatar_image.
func TestTracing_DownloadImageIsChildSpan(t *testing.T) {
	svc, _, _, _ := newTestService()
	ctx := context.Background()

	a, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)

	recorder := recordSpans(t)

	res, err := svc.GetImage(ctx, a.ID, "original")
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	parent := spanByName(t, recorder, "get_avatar_image")
	child := spanByName(t, recorder, "download_image")

	require.Equal(t, parent.SpanContext().TraceID(), child.SpanContext().TraceID(), "спаны должны быть в одном трейсе")
	require.Equal(t, parent.SpanContext().SpanID(), child.Parent().SpanID(), "download_image должен быть дочерним для get_avatar_image")
}

func TestTracing_SoftDeleteIsChildSpan(t *testing.T) {
	svc, _, _, _ := newTestService()
	ctx := context.Background()

	a, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)

	recorder := recordSpans(t)

	require.NoError(t, svc.DeleteByID(ctx, a.ID, "user-1"))

	parent := spanByName(t, recorder, "delete_avatar")
	child := spanByName(t, recorder, "soft_delete_avatar")

	require.Equal(t, parent.SpanContext().SpanID(), child.Parent().SpanID(), "soft_delete_avatar должен быть дочерним для delete_avatar")
}

// Ошибка во внутреннем шаге должна пометить и дочерний, и родительский спан:
// раньше родитель помечался ошибкой изнутри downloadImage через переданный
// параметр, теперь это делает сам вызывающий метод.
func TestTracing_DownloadErrorMarksBothSpans(t *testing.T) {
	svc, _, storage, _ := newTestService()
	ctx := context.Background()

	a, err := svc.Upload(ctx, "user-1", "avatar.jpg", "image/jpeg", 5, bytes.NewReader([]byte("hello")))
	require.NoError(t, err)

	// Файл пропал из хранилища — скачивание завершится ошибкой.
	delete(storage.objects, a.S3Key)

	recorder := recordSpans(t)

	_, err = svc.GetImage(ctx, a.ID, "original")
	require.Error(t, err)

	require.Equal(t, "Error", spanByName(t, recorder, "download_image").Status().Code.String())
	require.Equal(t, "Error", spanByName(t, recorder, "get_avatar_image").Status().Code.String())
}
