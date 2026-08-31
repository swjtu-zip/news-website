FROM golang:1.26 AS build

WORKDIR /src/swjtu-archive
COPY --from=swjtu-cli . /SWJTU-CLI
COPY go.mod go.sum* ./
ARG GOPROXY=https://mirrors.tuna.tsinghua.edu.cn/go/|https://goproxy.cn|direct
ENV GOPROXY=${GOPROXY}
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/swjtu-archive ./cmd/swjtu-archive

FROM scratch
WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/swjtu-archive /app/swjtu-archive
ENV SWJTU_ARCHIVE_DATA=/data
ENV SWJTU_ARCHIVE_ADDR=:8080
VOLUME ["/data"]
EXPOSE 8080
USER 1000:1000
ENTRYPOINT ["/app/swjtu-archive"]
