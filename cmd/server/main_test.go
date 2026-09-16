package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

var (
	testEchoOnce sync.Once
	testEcho     *echo.Echo
)

// newTestEcho собирает Echo с боевой цепочкой middleware один раз на весь
// пакет: echoprometheus регистрирует метрики в глобальном реестре, и
// повторный вызов useMiddleware упал бы на дублирующейся регистрации.
func newTestEcho() *echo.Echo {
	testEchoOnce.Do(func() {
		testEcho = echo.New()
		useMiddleware(testEcho, 0, 0) // лимит выключен: тест проверяет Recover, а не 429
		testEcho.GET("/panic", func(echo.Context) error {
			panic("паника в обработчике")
		})
		// Обработчик ничего не пишет в ответ: так паника в логе доступа,
		// который отрабатывает уже после обработчика, случится до отправки
		// ответа, и внешний Recover сможет вернуть 500.
		testEcho.GET("/quiet", func(echo.Context) error { return nil })
	})
	return testEcho
}

// withDefaultLogger временно подменяет логгер по умолчанию.
func withDefaultLogger(t *testing.T, h slog.Handler) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// Паника в обработчике должна стать обычным 500, который видят лог доступа и
// метрики. Если бы Recover стоял только снаружи цепочки, паника пролетела бы
// сквозь лог и метрики, и этот 500 нигде бы не отразился.
func TestMiddleware_HandlerPanicIsObservable(t *testing.T) {
	e := newTestEcho()

	var logs bytes.Buffer
	withDefaultLogger(t, slog.NewJSONHandler(&logs, nil))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)

	// Лог доступа записал запрос с 500 и уровнем ERROR.
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["msg"] == "request" && entry["uri"] == "/panic" {
			found = true
			require.Equal(t, "ERROR", entry["level"])
			require.EqualValues(t, 500, entry["status"])
		}
	}
	require.True(t, found, "паника обработчика должна попасть в лог доступа; логи: %s", logs.String())

	// И в HTTP-метрики с кодом 500.
	require.Equal(t, 1.0, requestsTotal(t, "/panic", "500"))
}

// Паника в самой цепочке middleware (здесь — в LogValuesFunc лога доступа,
// ровно сценарий из ревью) должна перехватываться внешним Recover, а не
// рвать соединение.
func TestMiddleware_PanicInMiddlewareIsRecovered(t *testing.T) {
	e := newTestEcho()
	withDefaultLogger(t, panickingHandler{})

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/quiet", nil))
	})
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// panickingHandler — slog.Handler, который паникует при записи, чтобы
// спровоцировать панику внутри LogValuesFunc.
type panickingHandler struct{}

func (panickingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (panickingHandler) Handle(context.Context, slog.Record) error {
	panic("паника в логгере")
}
func (h panickingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h panickingHandler) WithGroup(string) slog.Handler      { return h }

// requestsTotal читает счётчик avatar_requests_total из глобального реестра.
func requestsTotal(t *testing.T, url, code string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "avatar_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["url"] == url && labels["code"] == code {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}
