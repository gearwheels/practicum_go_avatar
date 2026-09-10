package observability

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Статусы для лейбла status в бизнес-метриках.
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// Бизнес-метрики сервиса аватарок.
var (
	UploadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "avatars_uploads_total",
			Help: "Total number of avatar uploads",
		},
		[]string{"status"},
	)

	UploadDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "avatars_upload_duration_seconds",
			Help:    "Avatar upload duration",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"},
	)

	// StorageUsage разбит по пользователям согласно ТЗ. В реальном проде
	// лейбл user_id — источник неограниченной кардинальности (по временному
	// ряду на каждого пользователя), для продакшена его стоило бы заменить
	// на общий счётчик объёма.
	StorageUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "avatars_storage_bytes",
			Help: "Total storage used by avatars",
		},
		[]string{"user_id"},
	)

	DeletesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "avatars_deletes_total",
			Help: "Total number of avatar deletions",
		},
		[]string{"status"},
	)

	DownloadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "avatars_downloads_total",
			Help: "Total number of avatar downloads by requested size",
		},
		[]string{"size", "status"},
	)
)

// Метрики фоновой обработки (воркер).
var (
	ThumbnailsGeneratedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "avatars_thumbnails_generated_total",
			Help: "Total number of generated avatar thumbnail sets",
		},
		[]string{"status"},
	)

	ThumbnailDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "avatars_thumbnail_duration_seconds",
			Help:    "Duration of thumbnail generation for one avatar",
			Buckets: prometheus.DefBuckets,
		},
	)

	WorkerMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_messages_total",
			Help: "Total number of broker messages handled by the worker",
		},
		[]string{"queue", "status"},
	)

	WorkerMessageDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "worker_message_duration_seconds",
			Help:    "Broker message handling duration",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"queue"},
	)
)

// ObserveUpload разом фиксирует счётчик и длительность загрузки — чтобы на
// стороне вызова не разъезжались лейблы status.
func ObserveUpload(start time.Time, err error) {
	status := statusLabel(err)
	UploadsTotal.WithLabelValues(status).Inc()
	UploadDuration.WithLabelValues(status).Observe(time.Since(start).Seconds())
}

// statusLabel приводит ошибку к значению лейбла status.
func statusLabel(err error) string {
	if err != nil {
		return StatusError
	}
	return StatusSuccess
}

// Handler отдаёт HTTP-обработчик реестра Prometheus (эндпоинт /metrics).
func Handler() http.Handler {
	return promhttp.Handler()
}

// dbPoolCollector публикует статистику пула соединений pgx как метрики
// Prometheus. Значения читаются в момент скрейпа, поэтому отдельный
// фоновый обновлятель не нужен.
type dbPoolCollector struct {
	pool *pgxpool.Pool

	total    *prometheus.Desc
	acquired *prometheus.Desc
	idle     *prometheus.Desc
	max      *prometheus.Desc
}

// RegisterDBPoolMetrics регистрирует коллектор статистики пула соединений.
func RegisterDBPoolMetrics(pool *pgxpool.Pool) error {
	return prometheus.Register(&dbPoolCollector{
		pool:     pool,
		total:    prometheus.NewDesc("db_pool_total_conns", "Total number of connections in the pgx pool", nil, nil),
		acquired: prometheus.NewDesc("db_pool_acquired_conns", "Number of currently acquired connections", nil, nil),
		idle:     prometheus.NewDesc("db_pool_idle_conns", "Number of currently idle connections", nil, nil),
		max:      prometheus.NewDesc("db_pool_max_conns", "Maximum number of connections allowed in the pool", nil, nil),
	})
}

func (c *dbPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.total
	ch <- c.acquired
	ch <- c.idle
	ch <- c.max
}

func (c *dbPoolCollector) Collect(ch chan<- prometheus.Metric) {
	stat := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, float64(stat.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.acquired, prometheus.GaugeValue, float64(stat.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(stat.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.max, prometheus.GaugeValue, float64(stat.MaxConns()))
}
