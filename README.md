# SWJTU 新闻归档

本仓库的应用位于 [`swjtu-archive/`](swjtu-archive/)，基于相邻的 [`../SWJTU-CLI`](../SWJTU-CLI) 公开 SDK 抓取并归档西南交通大学新闻。

```bash
cd swjtu-archive
GOCACHE=/tmp/swjtu-archive-gocache go test -vet=off ./...
go run ./cmd/swjtu-archive
```

归档数据默认写入 `swjtu-archive/data/`，包括 SQLite 数据库、原始页面和图片/附件。详见 [`swjtu-archive/README.md`](swjtu-archive/README.md)。
