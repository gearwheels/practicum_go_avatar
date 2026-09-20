# Техническое задание на спринт 3 — Kubernetes и Helm

## О спринте

В этом спринте фокус на работе с Kubernetes и Helm. В уроках используется локальный кластер на примере Rancher Desktop, но полученные навыки позволяют развернуть сервис в любом K8s-окружении.

Предстоит подготовить GophProfile к реальным нагрузкам: перенести инфраструктуру проекта в Kubernetes и упаковать её в Helm Chart, чтобы сделать деплой и управление сервисом максимально удобными.

## Задачи третьего этапа разработки GophProfile

### 1. Развернуть базовую инфраструктуру приложения в Kubernetes

Разработать манифесты для деплоя приложения: Deployment (с настройкой ресурсов и переменных окружения), Service и Ingress для маршрутизации трафика, а также ConfigMap и Secret для безопасного хранения конфигурации.

### 2. Обеспечить масштабируемость

Внедрить горизонтальное автомасштабирование (HPA) по CPU/RAM и настроить пробы жизнеспособности (Liveness/Readiness Probes).

### 3. Обеспечить мониторинг в Kubernetes

Создать ресурс ServiceMonitor, чтобы Prometheus мог автоматически обнаруживать поды и собирать метрики с эндпоинта `/metrics`.

### 4. Обеспечить безопасность

Настроить сетевые политики (NetworkPolicy) и ограничения прав доступа (RBAC/SecurityContext):

- ограничения на уровне подов;
- service account с минимальными правами;
- SecurityContext с non-root пользователем.

### 5. Упаковать проект в Helm Chart

- Templates для всех ресурсов
- Values файлы для разных окружений
- Хуки для миграций БД

### 6. Подготовить проект к продакшену

- обеспечить Graceful Shutdown;
- обновить README.md и описать, как запустить проект локально и как задеплоить в K8s;
- убедиться, что Swagger/OpenAPI спецификация актуальна;
- добавить схему архитектуры, включая K8s компоненты.

---

## Как это реализовано в проекте

### 1. Базовая инфраструктура

| Ресурс | Где |
|---|---|
| Deployment (server, worker) | [helm/gophprofile/templates/deployment-server.yaml](../helm/gophprofile/templates/deployment-server.yaml), [deployment-worker.yaml](../helm/gophprofile/templates/deployment-worker.yaml) |
| Service | [service.yaml](../helm/gophprofile/templates/service.yaml) |
| Ingress | [ingress.yaml](../helm/gophprofile/templates/ingress.yaml) |
| ConfigMap | [configmap.yaml](../helm/gophprofile/templates/configmap.yaml) |
| Secret | [secret.yaml](../helm/gophprofile/templates/secret.yaml) |
| PostgreSQL / RabbitMQ / MinIO | [infra-postgres.yaml](../helm/gophprofile/templates/infra-postgres.yaml), [infra-rabbitmq.yaml](../helm/gophprofile/templates/infra-rabbitmq.yaml), [infra-minio.yaml](../helm/gophprofile/templates/infra-minio.yaml) |

Готовые манифесты без Helm лежат в [k8s/](../k8s) — они генерируются из чарта скриптом [scripts/render-k8s.sh](../scripts/render-k8s.sh), чтобы не расходиться с ним.

Разделение конфигурации: всё, что содержит пароли (`DATABASE_URL`, `RABBITMQ_URL`, учётные данные MinIO), лежит в Secret; остальное — в ConfigMap. В продакшене Secret создаётся вне чарта и подключается через `secrets.existingSecret`.

Ingress помечен аннотацией `nginx.ingress.kubernetes.io/proxy-body-size: 10m` — без неё nginx резал бы тело запроса на 1 МБ, хотя сервис принимает файлы до 10 МБ.

### 2. Масштабируемость

HPA для обоих компонентов — [hpa.yaml](../helm/gophprofile/templates/hpa.yaml): CPU 70%, память 80%, `autoscaling/v2`. Добавлено поведение `scaleDown.stabilizationWindowSeconds: 300`, чтобы реплики не «схлопывались» на кратковременных провалах трафика.

Важная деталь: при включённом HPA поле `replicas` в Deployment **не рендерится** — иначе каждый `helm upgrade` возвращал бы количество реплик к значению из values, отменяя работу автомасштабирования.

**Пробы разделены по смыслу:**

- `readinessProbe` → `/health` — проверяет БД, S3 и брокер. Под выводится из балансировки, пока зависимости недоступны, и возвращается сам.
- `livenessProbe` → `/livez` — не делает сетевых вызовов. Использовать `/health` для liveness было бы опасно: при кратковременной недоступности Postgres kubelet перезапустил бы разом все реплики.
- `startupProbe` — даёт время на подключение к зависимостям при старте.

У воркера тоже есть пробы на порту метрик: без них он мог бы стать «зомби» — процесс жив и отдаёт метрики, но консьюмеры завершились и сообщения не обрабатываются.

### 3. Мониторинг

[servicemonitor.yaml](../helm/gophprofile/templates/servicemonitor.yaml) — отдельные ресурсы для сервера и воркера, под флагом `serviceMonitor.enabled` (требует CRD Prometheus Operator).

### 4. Безопасность

- **SecurityContext:** `runAsNonRoot`, UID 10001, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities: drop: [ALL]`, `seccompProfile: RuntimeDefault`. Под это пришлось переделать образ: раньше он работал от root с `WORKDIR /root/`, а этот каталог с правами 0700 для другого UID нечитаем.
- **ServiceAccount и RBAC:** [serviceaccount.yaml](../helm/gophprofile/templates/serviceaccount.yaml). Приложению не нужен доступ к API Kubernetes, поэтому токен в поды не монтируется (`automountServiceAccountToken: false`), а Role даёт только чтение собственной ConfigMap.
- **NetworkPolicy:** [networkpolicy.yaml](../helm/gophprofile/templates/networkpolicy.yaml) — default-deny плюс явные разрешения: входящий трафик к серверу только из namespace ingress-контроллера и от системы мониторинга, исходящий — только к своей инфраструктуре и DNS.

- **Пароли обязательны, дефолтов нет:** в `values.yaml` пароли пустые, а шаблоны подставляют их через хелперы с функцией `required` ([_helpers.tpl](../helm/gophprofile/templates/_helpers.tpl)). Установка без явных значений падает на рендере: дефолтный пароль, лежащий в репозитории, при забытом `--set` означал бы публично известные учётные данные в рабочем кластере. Сгенерированные манифесты в [k8s/](../k8s) содержат заглушки `CHANGE_ME`, в продакшене учётные данные приходят из внешнего Secret (`secrets.existingSecret`).
- **Ответы без внутренних деталей:** ни `500`, ни `503`, ни `400` не содержат текста оригинальной ошибки — иначе клиент увидел бы имя таблицы, SQLSTATE, адрес зависимости или подстроку DSN. Причина логируется на сервере с `trace_id` ([internal/api/handlers.go](../internal/api/handlers.go)).

> **Ограничение локального стенда:** политики применяет CNI. В kind политики применяются kindnet, в других окружениях поведение может отличаться — проверяйте, что ваш CNI поддерживает NetworkPolicy.

### 5. Helm Chart

Чарт в [helm/gophprofile](../helm/gophprofile): шаблоны всех ресурсов, `values.yaml` (база), `values-dev.yaml` (локальный kind) и `values-prod.yaml` (внешние managed-сервисы, TLS, внешний Secret).

**Хуки для миграций** — [job-migrate.yaml](../helm/gophprofile/templates/job-migrate.yaml). Для этого добавлен отдельный бинарь [cmd/migrate](../cmd/migrate/main.go), а поды сервера запускаются с `RUN_MIGRATIONS=false`: при нескольких репликах они стартовали бы наперегонки, и сбой посреди миграции оставил бы схему в состоянии `dirty`, после чего падали бы все поды.

Хук стоит на `post-install,pre-upgrade`, а не на `pre-install`: на чистой установке Postgres создаётся этим же чартом, а `pre-install`-хуки выполняются **до** применения манифестов — джоба ждала бы базу, которой ещё нет, и установка вставала бы намертво.

Второй хук — [job-minio-bucket.yaml](../helm/gophprofile/templates/job-minio-bucket.yaml), аналог `minio-init` из docker-compose. Бакет обязателен: проверка здоровья делает `BucketExists`, и без бакета `/health` отдавал бы 503 бесконечно.

### 6. Подготовка к продакшену

- **Graceful Shutdown** был реализован ещё в спринте 1; здесь он докручен под Kubernetes: `terminationGracePeriodSeconds` 45с для сервера (10с на дренаж HTTP + 5с на досылку трейсов + запас) и 60с для воркера, плюс `preStop: sleep 5`, чтобы kube-proxy успел убрать под из endpoints. Проверено: при замене пода 15 из 15 запросов прошли без ошибок.
- **README** — раздел про запуск локально и деплой в Kubernetes.
- **OpenAPI** — спека приведена в соответствие с кодом: добавлены `/livez` и `/metrics`, исправлен пример ответа `/health` 503 (был `degraded`, которого код никогда не возвращает), обновлён production-хост. `/livez` реализован через сгенерированный интерфейс, `/metrics` исключён из кодогенерации (`exclude-operation-ids`), так как его обслуживает middleware.
- **Схема архитектуры** — [docs/architecture.md](architecture.md), включая K8s-компоненты.

### Что нашлось при реальном деплое

Проверка на живом кластере (kind) вскрыла четыре проблемы, которых не было видно в docker-compose:

1. **NetworkPolicy блокировала Job'ы.** Поды миграций и создания бакета попадают под `default-deny` по общим меткам релиза, но правил для них не было — у них не работал даже DNS, и Job зависал на таймаутах резолва. Добавлена отдельная политика для джоб.
2. **Образы MinIO больше не тянутся с Docker Hub** (`pull access denied`). В docker-compose это не проявлялось только потому, что образ уже лежал в локальном кеше — на чистой машине сборка сломалась бы. Переведено на quay.io с закреплённым тегом, и в чарте, и в docker-compose.
3. **Пустой `OTEL_EXPORTER_OTLP_ENDPOINT` не выключал трейсинг.** `getEnv` трактовал пустую строку как «не задано» и подставлял `jaeger:4317`, поэтому в кластере без Jaeger поды безуспешно слали трейсы и писали ошибку при остановке — хотя README обещал обратное. Fallback убран.
4. **Хук миграций в `pre-install` создавал взаимную блокировку** на чистой установке (см. выше).
