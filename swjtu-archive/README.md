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
| `SWJTU_ARCHIVE_MAX_PAGES` | `1000` | 每个 feed 最多抓取页数 |
| `SWJTU_ARCHIVE_REFRESH_AFTER` | `24h` | 文章详情与资源的刷新间隔 |
| `SWJTU_ARCHIVE_SYNC_ON_START` | `true` | 是否启动时同步 |
| `SWJTU_ARCHIVE_ASSET_BASE_URL` | 空 | 资源公网基地址，例如 `https://oss.swjtu.zip/news-assets` |
| `SWJTU_ARCHIVE_R2_ENDPOINT` | 空 | R2 S3 账号端点（不含 bucket 路径） |
| `SWJTU_ARCHIVE_R2_BUCKET` | 空 | R2 bucket 名称 |
| `SWJTU_ARCHIVE_R2_PREFIX` | `news-assets` | R2 对象前缀 |
| `SWJTU_ARCHIVE_R2_ACCESS_KEY_ID` | 空 | R2 S3 访问密钥 ID；与 secret 一起设置才启用上传 |
| `SWJTU_ARCHIVE_R2_SECRET_ACCESS_KEY` | 空 | R2 S3 机密访问密钥 |

启用 R2 后，新下载的资源会先写入本地临时归档并上传到对象存储，上传成功后删除本地 `assets` 副本，才记为成功资源；新上传资源统一使用 `STANDARD`。历史资源可用 `go run ./cmd/r2sync` 进行可恢复迁移，已存在对象会被跳过。若需要将指定前缀下的历史对象统一改为标准存储，可运行 `go run ./cmd/r2sync --change-storage-class STANDARD --change-prefix news-assets/`；工具会先检查对象类别，只对 `STANDARD_IA` 对象使用 R2 服务端 CopyObject 转换。确认迁移完成后可加 `--delete-local`，工具只会在 R2 对象已存在或本次上传成功后删除对应本地文件。

## 访问

- Web：`http://localhost:8080/`
- API：`GET /api/v1/articles?q=人工智能&site=scai&from=2025-01-01&page=1`
- 站点：`GET /api/v1/feeds`
- 详情：`GET /api/v1/articles/{id}`
- 资源：`GET /assets/{id}`
- 同步状态：`GET /api/v1/sync/status`
- MCP：`http://localhost:8080/mcp`（Streamable HTTP）
- MCP 配置指南：`http://localhost:8080/help/mcp`

## MCP

将支持 Streamable HTTP 的 MCP 客户端连接到 `http://localhost:8080/mcp`。端点是无会话、只读的，支持当前 `2026-07-28` 协议，并保留 `2025-11-25`、`2025-06-18` 和 `2025-03-26` 客户端的握手兼容。它提供：

- `list_feeds`：查询可用新闻来源与过滤 ID
- `query_articles`：按站点、类别、Feed、日期和分页查询文章
- `search_articles`：对标题、正文和来源进行全文检索
- `get_article`：查看完整正文以及图片、附件列表
- `download_resource`：取得图片或附件的 MCP 资源 URI 与 HTTP 下载地址

客户端也可以读取 `swjtu://articles/{id}` 和 `swjtu://resources/{id}`；后者通过 MCP `resources/read` 返回 base64 编码的原始文件。大文件或需要保存到磁盘时，优先使用工具返回的 `/assets/{id}` HTTP 地址流式下载。

当前服务完全公开，不提供公网管理写接口。同步由后台定时器负责；后续可在独立内网端口增加管理接口。

## Docker

```bash
docker compose up -d --build
```

当前 Compose 配置将归档持久化到宿主机 `/data/swjtu-archive`；备份时停止服务后复制该目录即可。SQLite 数据库和资源目录一起构成完整归档。
