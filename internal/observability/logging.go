package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler оборачивает slog.Handler и дописывает в каждую запись
// идентификаторы текущего спана. Благодаря этому строку лога в Loki можно
// связать с трейсом в Jaeger.
//
// Важно: спан берётся из ctx, поэтому логировать нужно контекстными
// методами (InfoContext/ErrorContext) — обычные slog.Info передают внутрь
// context.Background(), в котором спана нет.
type traceHandler struct {
	slog.Handler
}

// Handle добавляет trace_id/span_id, если в контексте есть записываемый спан.
func (h traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanCtx.TraceID().String()),
			slog.String("span_id", spanCtx.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, record)
}

// WithAttrs и WithGroup переопределены, чтобы производные логгеры не теряли
// обёртку и продолжали дописывать trace_id.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{Handler: h.Handler.WithGroup(name)}
}

// NewLogger собирает JSON-логгер с корреляцией по трейсу и постоянным полем
// service — по нему логи фильтруются в Grafana.
func NewLogger(w io.Writer, serviceName, level string) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: ParseLogLevel(level)})
	return slog.New(traceHandler{Handler: handler}).With("service", serviceName)
}

// ParseLogLevel переводит значение LOG_LEVEL в slog.Level. Неизвестные
// значения трактуются как info.
func ParseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
