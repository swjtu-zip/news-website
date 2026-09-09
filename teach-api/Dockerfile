FROM golang:1.24-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .

RUN CGO_ENABLED=1 go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/teach-api ./cmd/teach-api

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1000 --create-home --shell /usr/sbin/nologin app \
    && mkdir -p /data \
    && chown 1000:1000 /data

WORKDIR /app
COPY --from=build /out/teach-api /app/teach-api

VOLUME ["/data"]
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/app/teach-api"]
