# SWJTU 新闻归档

本仓库的应用位于 [`swjtu-archive/`](swjtu-archive/)，基于相邻的 [`../SWJTU-CLI`](../SWJTU-CLI) 公开 SDK 抓取并归档西南交通大学新闻。

```bash
cd swjtu-archive
GOCACHE=/tmp/swjtu-archive-gocache go test -vet=off ./...
go run ./cmd/swjtu-archive
```

归档数据默认写入 `swjtu-archive/data/`，包括 SQLite 数据库、原始页面和图片/附件。详见 [`swjtu-archive/README.md`](swjtu-archive/README.md)。

## 相关仓库

教学与 MCP 组件已于 2026-10-06 拆分为组织内的独立仓库（均为 private）：

- `swjtu-zip/teach`：`teach-api` + `teach-web`，即 teach.swjtu.zip 站
- `swjtu-zip/teach-crawler`：公开教师目录/主页采集器
- `swjtu-zip/mcp`：聚合 MCP 网关（mcp.swjtu.zip），连接新闻与教学信息两个上游
