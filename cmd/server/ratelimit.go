package main

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"go-avatar-service/internal/api"
	"go-avatar-service/internal/resilience"
)

// unlimitedPaths — служебные эндпоинты, которые не ограничиваются: иначе
// при всплеске пользовательского трафика kubelet получил бы 429 на пробах
// и начал перезапускать поды, а Prometheus потерял бы метрики ровно в тот
// момент, когда они нужнее всего.
var unlimitedPaths = map[string]bool{
	"/health":  true,
	"/livez":   true,
	"/metrics": true,
}

// rateLimiter ограничивает частоту запросов в этом экземпляре сервиса.
//
// Ключ — X-User-ID, а без него — IP клиента: так один активный пользователь
// не выедает лимит соседей за одним NAT. Заголовок задаёт клиент, поэтому
// от целенаправленного флуда (смены X-User-ID) этот лимит не защищает — эту
// роль выполняет ограничение по IP на уровне ingress-nginx (см. аннотации
// limit-rps в values.yaml). Лимит считается на один под: при N репликах
// суммарная пропускная способность в N раз выше.
//
// rps <= 0 выключает ограничение.
func rateLimiter(rps float64, burst int) echo.MiddlewareFunc {
	if rps <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	if burst < 1 {
		burst = 1
	}

	// Через сколько секунд появится следующий токен — это и есть честный
	// Retry-After.
	retryAfter := strconv.Itoa(int(math.Max(1, math.Ceil(1/rps))))

	store := middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
		Rate:  rate.Limit(rps),
		Burst: burst,
		// Лимитеры неактивных клиентов удаляются, чтобы память не росла.
		ExpiresIn: 3 * time.Minute,
	})

	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Skipper: func(c echo.Context) bool {
			return unlimitedPaths[c.Request().URL.Path]
		},
		Store: store,
		IdentifierExtractor: func(c echo.Context) (string, error) {
			if userID := c.Request().Header.Get("X-User-ID"); userID != "" {
				return "user:" + userID, nil
			}
			return "ip:" + c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			c.Response().Header().Set("Retry-After", retryAfter)
			details := "Rate limit exceeded, retry later"
			return c.JSON(http.StatusTooManyRequests, api.Error{Error: "Too many requests", Details: &details})
		},
		ErrorHandler: func(c echo.Context, err error) error {
			return echo.NewHTTPError(http.StatusInternalServerError, "rate limiter failure").SetInternal(err)
		},
	})
}

// httpErrorHandler дополняет стандартный обработчик ошибок Echo: ошибка
// разомкнутого circuit breaker — временная недоступность зависимости, и
// отвечать на неё нужно 503, а не 500. Нужно для обработчиков, которые
// возвращают ошибку наверх (веб-страницы), — API-эндпоинты отвечают 503 сами.
func httpErrorHandler(e *echo.Echo) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if errors.Is(err, resilience.ErrCircuitOpen) {
			err = echo.NewHTTPError(http.StatusServiceUnavailable, "Service temporarily unavailable").SetInternal(err)
		}
		e.DefaultHTTPErrorHandler(err, c)
	}
}
