# Архитектура GophProfile

Документ описывает компоненты сервиса, потоки данных и то, как всё это
разворачивается в Kubernetes.

## Компоненты и потоки данных

```mermaid
flowchart TB
    client([Клиент<br/>браузер / стороннее приложение])

    subgraph app["Приложение"]
        server["server<br/>HTTP API + веб-интерфейс"]
        worker["worker<br/>фоновая обработка"]
    end

    subgraph infra["Инфраструктура"]
        pg[("PostgreSQL<br/>метаданные аватарок")]
        s3[("S3 / MinIO<br/>оригиналы и миниатюры")]
        mq{{"RabbitMQ<br/>avatars.events"}}
    end

    subgraph obs["Наблюдаемость"]
        prom["Prometheus<br/>метрики"]
        jaeger["Jaeger<br/>трейсы"]
        loki["Loki<br/>логи"]
        grafana["Grafana<br/>дашборды"]
    end

    client -->|"HTTPS"| server

    server -->|"1 сохранить оригинал"| s3
    server -->|"2 записать метаданные"| pg
    server -->|"3 событие avatar.process"| mq

    mq -->|"4 доставка события"| worker
    worker -->|"5 скачать оригинал"| s3
    worker -->|"6 загрузить миниатюры<br/>100x100, 300x300"| s3
    worker -->|"7 статус completed"| pg

    server -.->|"/metrics"| prom
    worker -.->|"/metrics"| prom
    server -.->|"OTLP"| jaeger
    worker -.->|"OTLP"| jaeger
    server -.->|"JSON-логи"| loki
    worker -.->|"JSON-логи"| loki
    prom --> grafana
    loki --> grafana
    jaeger --> grafana
```

Ключевая деталь: загрузка отвечает клиенту сразу после шага 3, а миниатюры
создаются асинхронно. Трейс при этом не обрывается — `traceparent`
передаётся в заголовках AMQP, поэтому шаги 1–7 видны одним трейсом.

## Развёртывание в Kubernetes

```mermaid
flowchart TB
    user([Пользователь])

    subgraph cluster["Кластер Kubernetes"]
        ingress["Ingress<br/>avatars.example.com<br/>proxy-body-size: 10m"]

        subgraph ns["namespace gophprofile"]
            svcServer["Service<br/>server:80"]
            svcWorker["Service<br/>worker:9091"]

            deployServer["Deployment server<br/>реплики 3<br/>readiness /health<br/>liveness /livez"]
            deployWorker["Deployment worker<br/>readiness /healthz<br/>liveness /livez"]

            hpaServer["HPA server<br/>CPU 70% / RAM 80%<br/>2-10 реплик"]
            hpaWorker["HPA worker<br/>1-6 реплик"]

            cm["ConfigMap<br/>несекретные настройки"]
            secret["Secret<br/>DSN и учётные данные"]

            jobMigrate["Job: миграции<br/>hook post-install / pre-upgrade"]
            jobBucket["Job: создание бакета<br/>hook post-install"]

            stsPg[("StatefulSet<br/>PostgreSQL + PVC")]
            stsMq[("StatefulSet<br/>RabbitMQ + PVC")]
            stsS3[("StatefulSet<br/>MinIO + PVC")]

            netpol["NetworkPolicy<br/>default-deny + разрешения"]
            sa["ServiceAccount<br/>токен не монтируется<br/>Role: read-only configmap"]
            sm["ServiceMonitor<br/>обнаружение подов Prometheus"]
        end
    end

    user --> ingress --> svcServer --> deployServer
    svcWorker --> deployWorker

    hpaServer -.->|масштабирует| deployServer
    hpaWorker -.->|масштабирует| deployWorker

    cm --> deployServer
    secret --> deployServer
    cm --> deployWorker
    secret --> deployWorker

    jobMigrate -->|схема БД| stsPg
    jobBucket -->|бакет| stsS3

    deployServer --> stsPg
    deployServer --> stsS3
    deployServer --> stsMq
    deployWorker --> stsPg
    deployWorker --> stsS3
    deployWorker --> stsMq

    netpol -.->|ограничивает| deployServer
    netpol -.->|ограничивает| deployWorker
    sa -.-> deployServer
    sm -.->|скрейп /metrics| svcServer
    sm -.-> svcWorker
```

### Почему сделано именно так

**Миграции отдельным Job, а не при старте сервера.** Когда реплик несколько,
все поды стартуют одновременно и гонятся за применение схемы. `golang-migrate`
берёт advisory-lock, поэтому схему они не испортят, но при сбое посреди
миграции в `schema_migrations` остаётся флаг `dirty`, и после этого падать
при старте будут уже **все** поды. Job выполняется один раз, его результат
виден отдельно, а поды сервера запускаются с `RUN_MIGRATIONS=false`.

Хук стоит на `post-install` (а не `pre-install`), потому что на чистой
установке Postgres создаётся этим же чартом: `pre-install` выполняется до
применения манифестов, и Job ждал бы базу, которой ещё нет. При обновлении
используется `pre-upgrade` — там база уже существует, и схема должна
обновиться до выката новых подов.

**Разные эндпоинты для readiness и liveness.** `/health` проверяет БД, S3 и
брокер и отдаёт 503, если хоть что-то недоступно, — это правильная семантика
для readiness: под выводится из балансировки и возвращается сам, когда
зависимости починятся. Ставить такую проверку в liveness опасно: при
кратковременной недоступности Postgres kubelet перезапустил бы разом все
реплики, добив систему, которой и так плохо. Поэтому liveness смотрит на
`/livez`, который не делает сетевых вызовов и реагирует только на
невосстановимое состояние процесса — закрытое AMQP-соединение (оно
устанавливается один раз и не переподключается).

**У воркера тоже есть пробы.** Без них он мог бы стать «зомби»: если
консьюмеры завершатся после перезапуска RabbitMQ, процесс продолжит жить и
отдавать метрики, не обрабатывая ни одного сообщения.

**Инфраструктура в чарте — только для стенда.** Для локального кластера
Postgres, RabbitMQ и MinIO поднимаются как StatefulSet с PVC. В продакшене
(`values-prod.yaml`) они выключены, а адреса managed-сервисов приходят через
внешний Secret.
