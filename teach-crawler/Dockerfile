FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
COPY vendor ./vendor
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/teach-crawler ./cmd/teach-crawler \
    && mkdir -p /runtime-data \
    && chown 1000:1000 /runtime-data

FROM scratch
WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/teach-crawler /app/teach-crawler
COPY --from=build --chown=1000:1000 /runtime-data /data
VOLUME ["/data"]
USER 1000:1000
ENTRYPOINT ["/app/teach-crawler"]
