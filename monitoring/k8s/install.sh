#!/usr/bin/env bash
# Устанавливает стек мониторинга в кластер: Prometheus Operator, Prometheus,
# Alertmanager, Grafana, kube-state-metrics и node-exporter.
#
# После этого:
#   - ServiceMonitor из чарта gophprofile подхватывается автоматически;
#   - PrometheusRule с алертами сервиса загружается в Prometheus;
#   - дашборды из ConfigMap с меткой grafana_dashboard появляются в Grafana.
#
# Использование:
#   ./monitoring/k8s/install.sh
set -euo pipefail

NAMESPACE="monitoring"
RELEASE="kube-prometheus-stack"
VALUES="$(dirname "$0")/kube-prometheus-stack-values.yaml"

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
helm repo update prometheus-community >/dev/null

helm upgrade --install "$RELEASE" prometheus-community/kube-prometheus-stack \
  --namespace "$NAMESPACE" --create-namespace \
  --values "$VALUES" \
  --wait --timeout 20m

echo
echo "Стек мониторинга установлен. Доступ:"
echo "  kubectl port-forward -n $NAMESPACE svc/$RELEASE-grafana 3001:80           # admin / admin"
echo "  kubectl port-forward -n $NAMESPACE svc/$RELEASE-prometheus 9091:9090"
echo "  kubectl port-forward -n $NAMESPACE svc/$RELEASE-alertmanager 9093:9093"
