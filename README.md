# SWJTU 新闻归档

本仓库的应用位于 [`swjtu-archive/`](swjtu-archive/)，基于相邻的 [`../SWJTU-CLI`](../SWJTU-CLI) 公开 SDK 抓取并归档西南交通大学新闻。

```bash
cd swjtu-archive
GOCACHE=/tmp/swjtu-archive-gocache go test -vet=off ./...
go run ./cmd/swjtu-archive
```

归档数据默认写入 `swjtu-archive/data/`，包括 SQLite 数据库、原始页面和图片/附件。详见 [`swjtu-archive/README.md`](swjtu-archive/README.md)。

## 教学信息项目

教学信息项目拆成三个独立部署单元：

- [`teach-crawler/`](teach-crawler/)：Go 编写的公开教师目录/主页采集器，输出 JSON 快照并提供 Dockerfile。
- [`teach-api/`](teach-api/)：预留的 Go API 服务。
- [`teach-web/`](teach-web/)：预留的前端项目。
