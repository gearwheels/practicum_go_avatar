# Шаблонный репозиторий для сервиса "Аватарница"

Это шаблонный репозиторий для выпускной работы по курсу "Go-разработчик". Он содержит базовую структуру проекта, готовую к дальнейшей разработке, а также техническое задание и пример веб-интерфейса.

## Описание

Проект "Аватарница" — это микросервис для управления аватарками пользователей. Он предоставляет REST API для загрузки, получения и удаления изображений, а также простой веб-интерфейс для взаимодействия с сервисом.

## Структура проекта

Проект имеет следующую структуру, основанную на лучших практиках разработки на Go:

```
/
├── cmd/                # Точки входа в приложение (main.go)
│   ├── server/         # HTTP-сервер
│   └── worker/         # Воркер для асинхронной обработки задач
├── internal/           # Внутренняя логика приложения
│   ├── api/            # Спецификации API (OpenAPI/Swagger)
│   ├── config/         # Конфигурация приложения
│   ├── domain/         # Основные доменные сущности
│   ├── handlers/       # HTTP-обработчики
│   ├── repository/     # Работа с хранилищем (БД, S3)
│   ├── services/       # Бизнес-логика
│   └── worker/         # Логика воркера
├── pkg/                # Публичные библиотеки, которые можно использовать в других проектах
├── web/                # Веб-интерфейс
│   └── static/         # Статические файлы (HTML, CSS, JS)
├── migrations/         # Миграции базы данных
├── docker/             # Docker-файлы и конфигурации
├── helm/               # Helm Chart (основной способ деплоя)
├── k8s/                # Манифесты Kubernetes (генерируются из чарта)
├── kind/               # Конфигурация локального кластера kind
├── monitoring/         # Конфигурация Prometheus/Grafana/Loki/Promtail
├── scripts/            # Вспомогательные скрипты
├── tests/              # Интеграционные и e2e тесты
├── docs/               # Документация проекта
└── .gitignore          # Файл для исключения файлов из Git
```

## Техническое задание

Подробное техническое задание находится в файлах
[docs/technical-specification.md](docs/technical-specification.md) (спринт 1 — MVP) и
[docs/technical-specification-sprint2.md](docs/technical-specification-sprint2.md)
(спринт 2 — наблюдаемость) и
[docs/technical-specification-sprint3.md](docs/technical-specification-sprint3.md)
(спринт 3 — Kubernetes и Helm).

Схема архитектуры, включая компоненты Kubernetes, —
[docs/architecture.md](docs/architecture.md).

## Как начать работу

1.  **Клонируйте репозиторий:**
    ```bash
    git clone <URL этого репозитория>
    cd go-avatar-service-template
    ```

2.  **Инициализируйте свой репозиторий на GitHub:**
    Следуйте инструкциям GitHub для создания нового репозитория и свяжите его с этим локальным репозиторием.

3.  **Установите зависимости:**
    ```bash
    go mod tidy
    ```

4.  **Настройте окружение:**
    ```bash
    cp .env.example .env
    ```
    При необходимости поменяйте значения (подключение к БД, S3, брокеру).

5.  **Запустите сервисы с помощью Docker Compose:**
    ```bash
    docker-compose up --build
    ```

После этого сервис будет доступен по адресу `http://localhost:8080`.

## Наблюдаемость

После `docker-compose up` вместе с сервисом поднимается стек наблюдаемости:

| Инструмент | Адрес | Для чего |
|---|---|---|
| Grafana | http://localhost:3000 | Дашборд «GophProfile — Avatar Service», просмотр логов (Explore → Loki) |
| Prometheus | http://localhost:9090 | Метрики и статус сбора (`/targets`) |
| Jaeger | http://localhost:16686 | Распределённые трейсы |
| Метрики сервера | http://localhost:8080/metrics | Сырые метрики HTTP-сервера |
| Метрики воркера | http://localhost:9091/metrics | Сырые метрики фонового обработчика |

Grafana открывается без логина (анонимный доступ с правами Viewer); для
изменения дашбордов — `admin` / `admin`.

**Что где смотреть:**

- **Трейсинг.** Каждый запрос порождает трейс со вложенными спанами: HTTP →
  бизнес-логика → SQL-запросы → операции с S3 → публикация в RabbitMQ. Трейс
  не обрывается на брокере: `traceparent` передаётся в заголовках сообщения,
  поэтому обработка в воркере видна тем же трейсом (`avatar-worker`).
- **Метрики.** Технические (RED: частота запросов, ошибки, длительность),
  инфраструктурные (соединения с БД, глубина очередей) и бизнесовые
  (`avatars_uploads_total`, `avatars_upload_duration_seconds`,
  `avatars_storage_bytes`).
- **Логи.** JSON от `slog` собираются Promtail'ом в Loki. В записях, сделанных
  во время обработки запроса, есть `trace_id` — в Grafana рядом с такой
  строкой появляется кнопка перехода в соответствующий трейс в Jaeger.

Трейсинг можно отключить, оставив `OTEL_EXPORTER_OTLP_ENDPOINT` пустым —
сервис продолжит работать без Jaeger.

## Деплой в Kubernetes

Основной способ развёртывания — Helm Chart в [helm/gophprofile](helm/gophprofile).
В каталоге [k8s/](k8s) лежат те же ресурсы обычными манифестами — они
генерируются из чарта (`./scripts/render-k8s.sh`) для тех, кто применяет
конфигурацию через `kubectl apply` без Helm. Редактировать `k8s/` вручную не
нужно: изменения потеряются при следующем рендере.

### Локальный кластер (kind)

Потребуются `docker`, `kubectl`, `helm` и `kind`.

```bash
# 1. Кластер с проброшенными портами 80/443 для Ingress
kind create cluster --config kind/cluster.yaml

# 2. Ingress-контроллер и metrics-server (нужен для HPA)
kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.12.0/deploy/static/provider/kind/deploy.yaml
# Отказ по лимиту запросов — 429, а не 503 по умолчанию (иначе его не
# отличить от недоступности сервиса):
kubectl patch configmap ingress-nginx-controller -n ingress-nginx --type merge \
  -p '{"data":{"limit-req-status-code":"429","limit-conn-status-code":"429"}}'
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
# В kind у kubelet самоподписанный сертификат:
kubectl patch deployment metrics-server -n kube-system --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

# 3. Мониторинг: Prometheus Operator, Prometheus, Alertmanager, Grafana,
#    kube-state-metrics, node-exporter (kube-prometheus-stack)
./monitoring/k8s/install.sh

# 4. Собрать образ и загрузить его в кластер
docker build -f docker/Dockerfile -t gophprofile:local .
kind load docker-image gophprofile:local --name gophprofile

# 5. Установить чарт (ServiceMonitor, алерты и дашборды ставятся вместе с ним)
helm install gophprofile helm/gophprofile \
  -f helm/gophprofile/values-dev.yaml \
  -n gophprofile --create-namespace
```

Проверка:

```bash
kubectl get pods -n gophprofile
curl -H "Host: avatars.local" http://localhost/health
```

Имя `avatars.local` можно добавить в hosts-файл (`127.0.0.1 avatars.local`) —
тогда сервис откроется в браузере напрямую.

### Что разворачивается

| Ресурс | Назначение |
|---|---|
| Deployment `server` | HTTP API и веб-интерфейс, автомасштабирование через HPA |
| Deployment `worker` | Фоновая генерация миниатюр и очистка S3 |
| StatefulSet `postgres` / `rabbitmq` / `minio` | Инфраструктура для стенда (в проде выключается) |
| Job (Helm hook) | Миграции БД и создание бакета |
| Ingress, Service | Маршрутизация трафика |
| ConfigMap, Secret | Конфигурация и учётные данные |
| HPA, PodDisruptionBudget | Масштабирование и устойчивость при обслуживании узлов |
| NetworkPolicy, RBAC, SecurityContext | Ограничение сети и прав |
| ServiceMonitor | Автообнаружение server, worker и RabbitMQ Prometheus'ом |
| PrometheusRule | Правила алертов (см. «Алерты») |
| ConfigMap с меткой `grafana_dashboard` | Дашборды Grafana: приложение и состояние в кластере |

ServiceMonitor и PrometheusRule создаются, только если в кластере есть CRD
Prometheus Operator, — без стека мониторинга чарт ставится как обычно.

### Окружения

| Файл | Для чего |
|---|---|
| `values.yaml` | База: инфраструктура в кластере, локальный образ |
| `values-dev.yaml` | Локальный kind: меньше ресурсов, debug-логи |
| `values-prod.yaml` | Продакшен: внешние managed-сервисы, TLS, `existingSecret`, образ из реестра |

```bash
# Продакшен-выкат (Secret с учётными данными создаётся вне чарта)
helm upgrade --install gophprofile helm/gophprofile \
  -f helm/gophprofile/values-prod.yaml \
  --set image.tag=1.0.0 --set ingress.host=avatars.example.com \
  -n gophprofile
```

### Полезные команды

```bash
kubectl get hpa -n gophprofile                      # автомасштабирование
kubectl logs -n gophprofile job/gophprofile-migrate  # результат миграций
helm history gophprofile -n gophprofile              # история релизов
helm rollback gophprofile -n gophprofile             # откат
```

### Мониторинг в кластере

Стек ставится скриптом [monitoring/k8s/install.sh](monitoring/k8s/install.sh)
(kube-prometheus-stack в namespace `monitoring`, значения —
[kube-prometheus-stack-values.yaml](monitoring/k8s/kube-prometheus-stack-values.yaml)).

```bash
kubectl port-forward -n monitoring svc/kube-prometheus-stack-grafana 3001:80           # admin / admin
kubectl port-forward -n monitoring svc/kube-prometheus-stack-prometheus 19090:9090
kubectl port-forward -n monitoring svc/kube-prometheus-stack-alertmanager 9093:9093
```

- **Сбор метрик.** Prometheus находит поды через ServiceMonitor
  (`gophprofile-server`, `-worker`, `-rabbitmq`). У ServiceMonitor и
  PrometheusRule есть метка `release: kube-prometheus-stack` — без неё
  стандартный Prometheus Operator их игнорирует. Если стек установлен под
  другим именем релиза, поменяйте `serviceMonitor.labels` и `alerts.labels`.
  Проверка: Prometheus → Status → Targets, у всех таргетов `UP`.
- **Дашборды** (Grafana → Dashboards, тег `gophprofile`):
  - *GophProfile — Avatar Service* — RED-метрики, пул БД, очереди, бизнес-KPI,
    состояние circuit breaker и отказы по rate limit. Тот же дашборд, что в
    docker-compose.
  - *GophProfile — состояние в кластере* — поды (фаза, готовность, узел,
    рестарты), HPA (текущие/желаемые/максимум реплик), реплики
    Deployment/StatefulSet, CPU и память подов относительно requests/limits,
    сеть, загрузка узлов.

### Алерты

Правила — в [templates/prometheusrule.yaml](helm/gophprofile/templates/prometheusrule.yaml),
пороги настраиваются в `alerts.*`. Сработавшие алерты видны в Prometheus →
Alerts и в Alertmanager.

| Алерт | Условие | Важность | Что делать |
|---|---|---|---|
| `GophProfileTargetDown` | Метрики server/worker не снимаются 2 мин | critical | Под упал или завис: `kubectl describe pod`, логи |
| `GophProfilePodNotReady` | Под не готов 5 мин | warning | Смотреть события пода и `/livez` |
| `GophProfilePodCrashLooping` | Больше 3 рестартов за 15 мин | critical | `kubectl logs --previous`, проверить OOMKilled и лимиты |
| `GophProfileHPAAtMaxReplicas` | HPA на максимуме реплик 15 мин | warning | Поднять `maxReplicas` или ёмкость узлов |
| `GophProfileHighErrorRate` | Доля 5xx выше 5% за 5 мин | critical | Логи в Grafana, трейсы, состояние зависимостей |
| `GophProfileHighLatencyP95` | p95 выше 1с 10 мин | warning | Панели длительности, пул БД, нагрузка на узлы |
| `GophProfileRateLimitRejections` | Больше 1 отказа 429 в секунду 10 мин | info | Злоупотребление или заниженный `config.rateLimitRPS` |
| `GophProfileCircuitBreakerOpen` | Брейкер Postgres/S3/RabbitMQ разомкнут дольше 1 мин | critical | Зависимость недоступна: `/health` покажет какая |
| `GophProfileStorageStatsDown` | Не считается занятое хранилище 5 мин | warning | Проверить доступность Postgres |
| `GophProfileQueueBacklog` | Больше 100 сообщений ждут в очереди 10 мин | warning | Воркеры не справляются или остановлены |

### Устойчивость

- **Rate limiting** в два рубежа. Ingress-nginx ограничивает частоту по IP
  клиента (`nginx.ingress.kubernetes.io/limit-rps`), приложение — по
  пользователю `X-User-ID`, а без заголовка по IP (`config.rateLimitRPS`,
  `config.rateLimitBurst`, лимит на под). Превышение — `429` с `Retry-After`.
  `/health`, `/livez` и `/metrics` не ограничиваются.
- **Circuit breaker** стоит на каждой внешней зависимости: PostgreSQL, S3,
  RabbitMQ (`internal/resilience`). После `config.circuitBreakerFailureThreshold`
  отказов подряд обращения к зависимости сразу отклоняются, и клиент мгновенно
  получает `503`, а не ждёт таймаута. Через `config.circuitBreakerOpenTimeout`
  проходит пробный запрос. Бизнес-ошибки («не найдено») брейкер не
  размыкают. Операции, которым сломанная зависимость не нужна, продолжают
  работать: при отказе S3 метаданные и списки отдаются.
  Состояние видно в метрике `circuit_breaker_state{name}`.
- **Таймауты.** Запрос ограничен 30 с. Для S3 заданы таймауты соединения и
  ожидания ответа, а повторов меньше, чем по умолчанию. У HTTP-сервера есть
  `ReadHeaderTimeout` (защита от Slowloris).
- **Пробы.** Readiness и liveness проверяют только сам процесс (`/livez`). Если
  бы readiness зависела от общего для всех реплик S3 или БД, его отказ разом
  вывел бы из балансировки все поды. `/health` с проверкой зависимостей нужен
  для мониторинга и диагностики.
- **Graceful shutdown.** `preStop` ждёт, пока под уберут из endpoints. Затем
  сервер дренирует соединения до 10 с, а воркер дообрабатывает текущее
  сообщение (`terminationGracePeriodSeconds` 45/60 с). PodDisruptionBudget не
  даёт одновременно вывести все реплики.

### Особенности стенда

- **Jaeger и Loki остаются в docker-compose.** В кластер вынесены приложение,
  его инфраструктура и метрики с алертами. Трейсинг в K8s включается
  параметром `config.otlpEndpoint`.
- **NetworkPolicy применяет CNI.** kindnet в kind 0.24+ политики поддерживает,
  так что на стенде изоляция реальная: запрос к серверу из чужого namespace
  не проходит. В продакшене нужен CNI с поддержкой политик (Calico, Cilium).
  Выход к внешним managed-сервисам разрешается через
  `networkPolicy.extraEgress`.
- **Миграции выполняет Helm-хук**, поды сервера стартуют с
  `RUN_MIGRATIONS=false`. Это нужно, чтобы несколько реплик не применяли схему
  наперегонки.

## Веб-интерфейс

Простой одностраничный веб-интерфейс для загрузки аватарок доступен по адресу `http://localhost:8080/`. Он находится в файле `web/static/index.html`.

**Важно:** Этот интерфейс предоставлен для облегчения старта и демонстрации работы API. Вы можете изменять его, адаптировать под свои нужды или полностью заменить на свой собственный фронтенд.