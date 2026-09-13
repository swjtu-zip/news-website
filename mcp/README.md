# swjtu-mcp

swjtu.zip 的聚合 MCP (Model Context Protocol) 网关。它作为客户端连接现有的两个无状态 MCP 上游服务,把它们的工具合并后通过统一的 `POST /mcp` 端点(streamable HTTP、无状态、JSON 响应)重新暴露,AI 助手只需配置一个入口即可同时访问新闻与教学信息两个数据源。

## 上游与工具前缀

| 上游 | 默认地址 | 前缀 | 内容 |
| --- | --- | --- | --- |
| news([`../swjtu-archive`](../swjtu-archive)) | `https://news.swjtu.zip/mcp` | `news_` | 学校各站点新闻/通知:list_feeds、query_articles、search_articles、get_article、download_resource |
| teach([`../teach-api`](../teach-api)) | `https://teach.swjtu.zip/mcp` | `teach_` | 教师目录与开课信息:search_teachers、get_teacher、search_courses、get_course_reviews、dataset_meta 等 |

工具的 Description 与 InputSchema 原样保留;调用时用原始(无前缀)名称转发给对应上游,结果(content、structuredContent、isError)原样返回。news 上游的资源模板(`swjtu://articles/{id}`、`swjtu://resources/{id}`)也一并透传,`resources/read` 转发给 news 会话。

启动时会立即连接两个上游;某个上游不可达只记警告并每 30s 后台重试,网关继续服务其余上游。上游无状态,每个上游只维持一个客户端会话,调用出现传输层错误时自动重连一次并重试。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MCP_GATEWAY_ADDR` | `:8080` | 监听地址 |
| `MCP_NEWS_URL` | `https://news.swjtu.zip/mcp` | news 上游 MCP 端点;compose 内网可用 `http://swjtu-archive:8080/mcp` |
| `MCP_TEACH_URL` | `https://teach.swjtu.zip/mcp` | teach 上游 MCP 端点;compose 内网可用 `http://teach-api:8080/mcp` |

## 本地运行

需要 Go 1.25+,依赖已 vendor:

```bash
cd mcp
go run -mod=vendor ./cmd/mcp
```

启动后:

- `GET /healthz`:健康检查,返回 `ok` 及每个上游的 up/down 状态。
- `POST /mcp`:MCP 端点。

```bash
curl http://localhost:8080/healthz
```

## 客户端接入

公网入口规划为 `https://mcp.swjtu.zip/mcp`。以支持 streamable HTTP 的 MCP 客户端为例:

```json
{
  "mcpServers": {
    "swjtu": {
      "type": "http",
      "url": "https://mcp.swjtu.zip/mcp"
    }
  }
}
```

连接后可见 `news_*` 与 `teach_*` 两组工具,以及 `swjtu://` 资源模板。

## Docker

```bash
docker build -t swjtu-mcp:local .
docker run --rm -p 8080:8080 swjtu-mcp:local
```

镜像内默认指向公网上游地址;在 compose/内网部署时通过环境变量覆盖为内部地址:

```yaml
services:
  mcp:
    build: ./mcp
    environment:
      MCP_NEWS_URL: http://swjtu-archive:8080/mcp
      MCP_TEACH_URL: http://teach-api:8080/mcp
    ports:
      - "8080:8080"
```

镜像运行时为非 root 用户,CGO 关闭,无本地状态。
