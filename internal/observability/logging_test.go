package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace"
)

// decodeLogLine разбирает единственную JSON-строку, записанную логгером.
func decodeLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	return record
}

func TestNewLogger_AddsTraceIDInsideSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "test-service", "info")

	// Настоящий SDK-провайдер: у no-op трейсера SpanContext невалиден, и
	// тест не проверял бы ничего.
	provider := trace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	ctx, span := provider.Tracer("test").Start(context.Background(), "op")
	logger.InfoContext(ctx, "внутри спана")
	spanCtx := span.SpanContext()
	span.End()

	record := decodeLogLine(t, &buf)
	require.Equal(t, spanCtx.TraceID().String(), record["trace_id"])
	require.Equal(t, spanCtx.SpanID().String(), record["span_id"])
	require.Equal(t, "test-service", record["service"])
	require.Equal(t, "внутри спана", record["msg"])
}

func TestNewLogger_NoTraceIDOutsideSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "test-service", "info")

	logger.InfoContext(context.Background(), "без спана")

	record := decodeLogLine(t, &buf)
	require.NotContains(t, record, "trace_id")
	require.NotContains(t, record, "span_id")
	require.Equal(t, "test-service", record["service"])
}

// Производный логгер (With) должен сохранять обёртку и продолжать дописывать
// trace_id — иначе корреляция потеряется в первом же слое, который добавил
// свои атрибуты.
func TestNewLogger_KeepsTraceCorrelationAfterWith(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "test-service", "info").With("component", "uploader")

	provider := trace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	ctx, span := provider.Tracer("test").Start(context.Background(), "op")
	logger.InfoContext(ctx, "с атрибутом")
	span.End()

	record := decodeLogLine(t, &buf)
	require.Contains(t, record, "trace_id")
	require.Equal(t, "uploader", record["component"])
}

func TestNewLogger_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "test-service", "error")

	logger.InfoContext(context.Background(), "не должно попасть в вывод")
	require.Empty(t, buf.String())

	logger.ErrorContext(context.Background(), "должно попасть")
	require.Contains(t, buf.String(), "должно попасть")
}

func TestParseLogLevel(t *testing.T) {
	require.Equal(t, slog.LevelDebug, ParseLogLevel("debug"))
	require.Equal(t, slog.LevelInfo, ParseLogLevel("INFO"))
	require.Equal(t, slog.LevelWarn, ParseLogLevel("warning"))
	require.Equal(t, slog.LevelError, ParseLogLevel(" error "))
	require.Equal(t, slog.LevelInfo, ParseLogLevel("нечто непонятное"))
}
