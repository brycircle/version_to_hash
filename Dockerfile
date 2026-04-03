FROM golang:1.23-alpine AS builder

WORKDIR /app

# Cache dependencies layer separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o version-to-hash ./cmd/server


FROM alpine:3.19

# ca-certificates is required for HTTPS calls to the GitHub API.
RUN apk --no-cache add ca-certificates && \
    mkdir -p /data

WORKDIR /app
COPY --from=builder /app/version-to-hash .

# /data is where BoltDB persists the cache across container restarts.
VOLUME ["/data"]

EXPOSE 8080

ENV CACHE_PATH=/data/cache.db \
    CACHE_TTL_HOURS=24 \
    PORT=8080 \
    LOG_LEVEL=info

ENTRYPOINT ["./version-to-hash"]
