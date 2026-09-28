package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/resilience"
)

func newLimitedEcho(rps float64, burst int) *echo.Echo {
	e := echo.New()
	e.Use(rateLimiter(rps, burst))
	ok := func(c echo.Context) error { return c.String(http.StatusOK, "ok") }
	e.GET("/api/v1/users/:id/avatars", ok)
	e.GET("/health", ok)
	e.GET("/livez", ok)
	e.GET("/metrics", ok)
	return e
}

func doGet(e *echo.Echo, path, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestRateLimiter_RejectsOverLimitWith429(t *testing.T) {
	// Крошечная частота, чтобы токены не успевали восстановиться за тест.
	e := newLimitedEcho(0.01, 2)

	require.Equal(t, http.StatusOK, doGet(e, "/api/v1/users/a/avatars", "alice").Code)
	require.Equal(t, http.StatusOK, doGet(e, "/api/v1/users/a/avatars", "alice").Code)

	rec := doGet(e, "/api/v1/users/a/avatars", "alice")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "100", rec.Header().Get("Retry-After"), "Retry-After = время до следующего токена")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "Too many requests", body["error"])
}

// Лимит считается на клиента: один активный пользователь не выедает лимит
// другого.
func TestRateLimiter_LimitsArePerUser(t *testing.T) {
	e := newLimitedEcho(0.01, 1)

	require.Equal(t, http.StatusOK, doGet(e, "/api/v1/users/a/avatars", "alice").Code)
	require.Equal(t, http.StatusTooManyRequests, doGet(e, "/api/v1/users/a/avatars", "alice").Code)
	require.Equal(t, http.StatusOK, doGet(e, "/api/v1/users/a/avatars", "bob").Code)
}

// Пробы и скрейп метрик не ограничиваются, иначе при всплеске трафика
// kubelet перезапускал бы поды, а мониторинг терял бы данные.
func TestRateLimiter_SkipsProbesAndMetrics(t *testing.T) {
	e := newLimitedEcho(0.01, 1)
	require.Equal(t, http.StatusOK, doGet(e, "/api/v1/users/a/avatars", "").Code)

	for _, path := range []string{"/health", "/livez", "/metrics"} {
		for i := 0; i < 5; i++ {
			require.Equal(t, http.StatusOK, doGet(e, path, "").Code, path)
		}
	}
}

func TestRateLimiter_DisabledWhenRPSNotPositive(t *testing.T) {
	e := newLimitedEcho(0, 0)
	for i := 0; i < 50; i++ {
		require.Equal(t, http.StatusOK, doGet(e, "/api/v1/users/a/avatars", "alice").Code)
	}
}

// Ошибка разомкнутого брейкера, возвращённая обработчиком наверх (так
// работают веб-страницы), превращается в 503, а не в 500.
func TestHTTPErrorHandler_CircuitOpenIs503(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httpErrorHandler(e)
	e.GET("/web/gallery/:id", func(echo.Context) error {
		return fmt.Errorf("галерея: %w", resilience.ErrCircuitOpen)
	})
	e.GET("/boom", func(echo.Context) error { return fmt.Errorf("что-то сломалось") })

	require.Equal(t, http.StatusServiceUnavailable, doGet(e, "/web/gallery/alice", "").Code)
	require.Equal(t, http.StatusInternalServerError, doGet(e, "/boom", "").Code)
}
