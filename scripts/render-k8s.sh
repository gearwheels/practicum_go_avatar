#!/usr/bin/env bash
# Генерирует простые манифесты Kubernetes в k8s/ из Helm-чарта.
#
# Единый источник правды — чарт в helm/gophprofile. Каталог k8s/ нужен для
# тех, кто хочет применить конфигурацию обычным `kubectl apply` без Helm,
# и для чтения на ревью. Не редактируйте k8s/ вручную: изменения потеряются
# при следующем запуске этого скрипта.
#
# Использование:
#   ./scripts/render-k8s.sh
set -euo pipefail

CHART_DIR="helm/gophprofile"
OUT_DIR="k8s"
RELEASE="gophprofile"
NAMESPACE="gophprofile"
# Заглушка вместо паролей в сгенерированном Secret: реальные значения
# передаются при установке чарта и в репозитории не хранятся.
PASSWORD_PLACEHOLDER="CHANGE_ME"

command -v helm >/dev/null 2>&1 || { echo "helm не найден в PATH" >&2; exit 1; }

# Дашборд приложения — один на docker-compose и Kubernetes. Helm читает
# только файлы внутри чарта, поэтому держим в чарте копию.
cp monitoring/grafana/dashboards/avatar-service.json "$CHART_DIR/dashboards/avatar-service.json"

rm -rf "${OUT_DIR:?}"/*.yaml
mkdir -p "$OUT_DIR"

echo "Рендерю $CHART_DIR -> $OUT_DIR ..."

# --output-dir раскладывает ресурсы по отдельным файлам, повторяя структуру
# templates/ — так манифесты удобнее читать и применять выборочно.
# --api-versions: helm template не видит кластер, а ServiceMonitor и
# PrometheusRule рендерятся только при наличии CRD Prometheus Operator.
#
# Паролей по умолчанию в чарте нет, поэтому здесь подставляются плейсхолдеры:
# сгенерированные манифесты — материал для чтения и для `kubectl apply` на
# стенде, а не готовый к применению Secret. Реальные пароли задаются при
# установке чарта и в репозиторий не попадают.
helm template "$RELEASE" "$CHART_DIR" \
  --namespace "$NAMESPACE" \
  --values "$CHART_DIR/values.yaml" \
  --set secrets.postgresPassword="$PASSWORD_PLACEHOLDER" \
  --set secrets.rabbitmqPassword="$PASSWORD_PLACEHOLDER" \
  --set secrets.minioPassword="$PASSWORD_PLACEHOLDER" \
  --api-versions monitoring.coreos.com/v1/ServiceMonitor \
  --api-versions monitoring.coreos.com/v1/PrometheusRule \
  --output-dir "$OUT_DIR.tmp" >/dev/null

# Переносим из вложенной структуры (<chart>/templates/*.yaml) в плоский k8s/
find "$OUT_DIR.tmp" -name '*.yaml' -exec mv {} "$OUT_DIR"/ \;
rm -rf "$OUT_DIR.tmp"

# Шапка с пометкой о генерации
for f in "$OUT_DIR"/*.yaml; do
  tmp="$f.tmp"
  {
    echo "# СГЕНЕРИРОВАНО автоматически из helm/gophprofile — не редактируйте вручную."
    echo "# Обновить: ./scripts/render-k8s.sh"
    echo "#"
    echo "# Значения взяты из helm/gophprofile/values.yaml. Для другого окружения"
    echo "# используйте Helm напрямую с нужным values-файлом."
    if grep -q "^kind: Secret" "$f"; then
      echo "#"
      echo "# ВНИМАНИЕ: пароли здесь — заглушки $PASSWORD_PLACEHOLDER. Перед kubectl apply"
      echo "# подставьте свои значения или создайте Secret отдельно."
    fi
    cat "$f"
  } > "$tmp"
  mv "$tmp" "$f"
done

echo "Готово. Файлов: $(find "$OUT_DIR" -name '*.yaml' | wc -l)"
echo "Применить: kubectl apply -n $NAMESPACE -f $OUT_DIR/"
