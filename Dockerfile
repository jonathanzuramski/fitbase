# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /fitbase ./cmd/fitbase

# ── Runtime stage ────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata su-exec

COPY --from=build /fitbase /usr/local/bin/fitbase
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV FITBASE_DB_PATH=/data/fitbase.db \
    FITBASE_KEY_PATH=/data/master.key \
    FITBASE_WATCH_DIR=/data/watch \
    FITBASE_ARCHIVE_DIR=/data/archive \
    FITBASE_PORT=8780

VOLUME /data
EXPOSE 8780

ENTRYPOINT ["/entrypoint.sh"]
