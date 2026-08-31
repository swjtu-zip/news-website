# SWJTU 新闻归档

这是一个基于 `../SWJTU-CLI` SDK 的新闻归档服务。它会同步已注册的校级、学院、部门、直属单位和研究院新闻，保存正文、原始 HTML、图片与附件，并提供公开的 Web 阅读和 JSON API。

## 本地运行

```bash
go run ./cmd/swjtu-archive
```

默认监听 `:8080`，数据写入 `./data`。服务启动后会立即执行一次同步，之后按间隔增量同步。

常用环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SWJTU_ARCHIVE_DATA` | `./data` | SQLite、原始页面和资源目录 |
| `SWJTU_ARCHIVE_ADDR` | `:8080` | HTTP 监听地址 |
| `SWJTU_ARCHIVE_INTERVAL` | `30m` | 自动同步间隔 |
| `SWJTU_ARCHIVE_BACKFILL` | `8760h` | 首次/每次同步的回溯窗口 |
| `SWJTU_ARCHIVE_MAX_PAGES` | `100` | 每个 feed 最多抓取页数 |
| `SWJTU_ARCHIVE_SYNC_ON_START` | `true` | 是否启动时同步 |

## 访问

- Web：`http://localhost:8080/`
- API：`GET /api/v1/articles?q=人工智能&site=scai&from=2025-01-01&page=1`
- 站点：`GET /api/v1/feeds`
- 详情：`GET /api/v1/articles/{id}`
- 资源：`GET /assets/{id}`
- 同步状态：`GET /api/v1/sync/status`

当前服务完全公开，不提供公网管理写接口。同步由后台定时器负责；后续可在独立内网端口增加管理接口。

## Docker

```bash
docker compose up -d --build
```

当前 Compose 配置将归档持久化到宿主机 `/data/swjtu-archive`；备份时停止服务后复制该目录即可。SQLite 数据库和资源目录一起构成完整归档。
