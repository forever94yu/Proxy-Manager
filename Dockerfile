FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-bookworm AS api-builder
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/proxy-manager .

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /app --shell /usr/sbin/nologin proxy-manager \
    && mkdir -p /app/web /data \
    && chown -R proxy-manager:proxy-manager /app /data

WORKDIR /app
COPY --from=api-builder /out/proxy-manager /app/proxy-manager
COPY --from=web-builder /src/web/dist/ /app/web/
COPY --chown=proxy-manager:proxy-manager 3proxy-install.sh /app/3proxy-install.sh

ENV APP_ENV=production \
    HTTP_ADDR=:8080 \
    DB_PATH=/data/proxy-manager.db \
    EXECUTOR_MODE=ssh \
    INSTALL_SCRIPT_PATH=/app/3proxy-install.sh \
    STATIC_DIR=/app/web

VOLUME ["/data"]
EXPOSE 8080
USER proxy-manager
ENTRYPOINT ["/app/proxy-manager"]
