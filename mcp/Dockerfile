FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .

RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/swjtu-mcp ./cmd/mcp

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1000 --create-home --shell /usr/sbin/nologin app

USER 1000:1000
COPY --from=build /out/swjtu-mcp /app/swjtu-mcp

ENV MCP_GATEWAY_ADDR=:8080 \
    MCP_NEWS_URL=https://news.swjtu.zip/mcp \
    MCP_TEACH_URL=https://teach.swjtu.zip/mcp

EXPOSE 8080
ENTRYPOINT ["/app/swjtu-mcp"]
