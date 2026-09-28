# Build stage
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o worker ./cmd/worker
# Отдельный бинарь миграций: его запускает Job-хук Helm перед выкатом подов
RUN CGO_ENABLED=0 GOOS=linux go build -o migrate ./cmd/migrate

# Runtime stage
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

# Непривилегированный пользователь: в Kubernetes поды запускаются с
# runAsNonRoot. Рабочий каталог /app, а не /root — домашний каталог root
# имеет права 0700 и для другого UID нечитаем.
RUN addgroup -g 10001 -S app && adduser -u 10001 -S app -G app

WORKDIR /app
COPY --from=builder --chown=app:app /app/server .
COPY --from=builder --chown=app:app /app/worker .
COPY --from=builder --chown=app:app /app/migrate .
COPY --from=builder --chown=app:app /app/web ./web/
COPY --from=builder --chown=app:app /app/migrations ./migrations/

USER app:app

EXPOSE 8080 9091

# По умолчанию запускается HTTP-сервер; для воркера и миграций команда
# переопределяется (docker-compose.yml, Helm-шаблоны).
CMD ["./server"]
