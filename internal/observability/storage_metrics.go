package observability

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// storageQueryTimeout ограничивает запрос к БД во время скрейпа: Prometheus
// не должен ждать медленную базу дольше собственного scrape_timeout.
const storageQueryTimeout = 5 * time.Second

// StorageUsageSource отдаёт суммарный объём аватарок по пользователям.
// Реализуется репозиторием (postgres.AvatarRepository); интерфейс объявлен
// здесь, чтобы пакет наблюдаемости не зависел от слоя хранения.
type StorageUsageSource interface {
	StorageUsageByUser(ctx context.Context) (map[string]int64, error)
}

// storageUsageCollector считает avatars_storage_bytes из БД в момент скрейпа.
//
// Раньше метрика была GaugeVec в памяти процесса, которую сервис увеличивал
// при загрузке и уменьшал при удалении. Для бизнес-KPI это ненадёжно:
// после рестарта значение обнулялось, а при сбое между записью в БД и
// обновлением счётчика расходилось с реальностью навсегда. База — источник
// правды, поэтому значение берётся из неё.
type storageUsageCollector struct {
	source StorageUsageSource

	bytes *prometheus.Desc
	up    *prometheus.Desc
}

// NewStorageUsageCollector создаёт коллектор объёма хранилища.
func NewStorageUsageCollector(source StorageUsageSource) prometheus.Collector {
	return &storageUsageCollector{
		source: source,
		// Лейбл user_id — требование ТЗ. На большой базе это неограниченная
		// кардинальность (по ряду на пользователя); в продакшене стоило бы
		// оставить только общий объём.
		bytes: prometheus.NewDesc(
			"avatars_storage_bytes",
			"Total size of original avatar files per user, computed from the database (soft-deleted avatars excluded)",
			[]string{"user_id"}, nil,
		),
		up: prometheus.NewDesc(
			"avatars_storage_stats_up",
			"1 if avatars_storage_bytes was successfully computed from the database on this scrape, 0 otherwise",
			nil, nil,
		),
	}
}

// RegisterStorageUsageMetrics регистрирует коллектор в реестре по умолчанию.
//
// Регистрировать его нужно в одном процессе (сервере): значение одно на всю
// базу, и если его отдавали бы и сервер, и воркер, ряды дублировались бы.
func RegisterStorageUsageMetrics(source StorageUsageSource) error {
	return prometheus.Register(NewStorageUsageCollector(source))
}

func (c *storageUsageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bytes
	ch <- c.up
}

func (c *storageUsageCollector) Collect(ch chan<- prometheus.Metric) {
	// Collect не принимает контекст — ограничиваем запрос таймаутом сами.
	ctx, cancel := context.WithTimeout(context.Background(), storageQueryTimeout)
	defer cancel()

	usage, err := c.source.StorageUsageByUser(ctx)
	if err != nil {
		// Не отдаём prometheus.NewInvalidMetric: с ним promhttp вернул бы 500
		// на весь /metrics, и вместе с этой метрикой пропали бы все остальные —
		// причём именно во время проблем с БД, когда они нужнее всего.
		// Вместо этого сообщаем о сбое отдельным рядом.
		slog.Error("не удалось посчитать объём хранилища для метрик", "error", err)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}

	for userID, size := range usage {
		ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, float64(size), userID)
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)
}
