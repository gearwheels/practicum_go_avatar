package observability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type fakeStorageSource struct {
	usage map[string]int64
	err   error
	calls int
}

func (f *fakeStorageSource) StorageUsageByUser(context.Context) (map[string]int64, error) {
	f.calls++
	return f.usage, f.err
}

func TestStorageUsageCollector_ReportsValuesFromSource(t *testing.T) {
	source := &fakeStorageSource{usage: map[string]int64{"alice": 2709, "bob": 1024}}

	expected := `
# HELP avatars_storage_bytes Total size of original avatar files per user, computed from the database (soft-deleted avatars excluded)
# TYPE avatars_storage_bytes gauge
avatars_storage_bytes{user_id="alice"} 2709
avatars_storage_bytes{user_id="bob"} 1024
# HELP avatars_storage_stats_up 1 if avatars_storage_bytes was successfully computed from the database on this scrape, 0 otherwise
# TYPE avatars_storage_stats_up gauge
avatars_storage_stats_up 1
`
	require.NoError(t, testutil.CollectAndCompare(NewStorageUsageCollector(source), strings.NewReader(expected)))
}

// Значение не хранится в памяти процесса: каждый скрейп заново спрашивает
// источник. Поэтому после рестарта метрика сразу верная, а не нулевая, и
// сама собой сходится с базой после любого сбоя.
func TestStorageUsageCollector_ReadsSourceOnEveryScrape(t *testing.T) {
	source := &fakeStorageSource{usage: map[string]int64{"alice": 100}}
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(NewStorageUsageCollector(source)))

	require.Equal(t, 100.0, gaugeValue(t, registry, "avatars_storage_bytes"))

	// «В базе» что-то поменялось (удаление, сбой посреди операции) —
	// следующий скрейп видит актуальное значение без участия сервиса.
	source.usage = map[string]int64{"alice": 40}
	require.Equal(t, 40.0, gaugeValue(t, registry, "avatars_storage_bytes"))
	require.Equal(t, 2, source.calls)
}

// При ошибке БД коллектор не ломает весь /metrics: отдаёт только признак
// сбоя, а остальные метрики реестра продолжают собираться.
func TestStorageUsageCollector_SourceErrorDoesNotBreakScrape(t *testing.T) {
	source := &fakeStorageSource{err: errors.New("база недоступна")}

	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(NewStorageUsageCollector(source)))
	other := prometheus.NewCounter(prometheus.CounterOpts{Name: "other_metric_total", Help: "unrelated"})
	other.Inc()
	require.NoError(t, registry.Register(other))

	families, err := registry.Gather()
	require.NoError(t, err, "сбой источника не должен превращаться в ошибку всего скрейпа")

	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	require.True(t, names["other_metric_total"], "остальные метрики должны собираться")
	require.False(t, names["avatars_storage_bytes"], "неверных значений объёма при сбое быть не должно")
	require.Equal(t, 0.0, gaugeValue(t, registry, "avatars_storage_stats_up"))
}

func gaugeValue(t *testing.T, g prometheus.Gatherer, name string) float64 {
	t.Helper()
	families, err := g.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			require.Len(t, f.GetMetric(), 1)
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	require.Failf(t, "метрика не найдена", "%s", name)
	return 0
}
