# Многостадийная сборка: статический бинарь сервера в build-стадии, минимальный
# distroless-образ на выходе (nonroot, без shell — меньше поверхность атаки).
FROM golang:1.25-alpine AS build
WORKDIR /src

# Слой зависимостей кэшируется отдельно от исходников.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO выключен → полностью статический бинарь; -trimpath и -s -w убирают пути и
# отладочные символы.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
# Клиент (index.html + game.js) отдаётся сервером на "/".
COPY web /app/web

# Адрес игры — :8080; каталог клиента — /app/web. pprof остаётся на 127.0.0.1:6060
# ВНУТРИ контейнера (пустой ARENA_PPROF_ADDR трактуется как дефолт, а не «выкл»);
# наружу он не публикуется — EXPOSE только 8080.
ENV ARENA_ADDR=:8080 \
    ARENA_WEB_DIR=/app/web
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
