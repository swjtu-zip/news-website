# teach-api

独立的 Go + SQLite 数据服务。它读取 `teach-crawler` 生成的 JSON 快照，提供教师主页所需的公开基本资料、联系方式、办公地点、头像和详细栏目；爬虫状态与原始 HTML 不会进入 API 响应。开课信息(选修课教学班)保存在独立的 `courses` 表，通过 `-import-courses` 导入 `scripts/xlsx_to_courses.py` 生成的快照，并按教师姓名与教师目录自动关联。历史成绩应在后续接入独立的数据表与认证接口。

## 本地运行

需要 Go 1.24+ 和 CGO（SQLite 驱动使用 `go-sqlite3`）：

```bash
cd teach-api
go run ./cmd/teach-api \
  -db ./data/teach.db \
  -import ../teach-crawler/data/teachers.json
```

只导入并退出可使用 `-import-only`：

```bash
go run ./cmd/teach-api \
  -db ./data/teach.db \
  -import ../teach-crawler/data/teachers.json \
  -import-only
```

启动后可访问：

```bash
curl 'http://localhost:8080/api/v1/teachers?page=1&page_size=24'
curl 'http://localhost:8080/api/v1/meta'
curl 'http://localhost:8080/api/v1/healthz'
```

主要接口：

- `GET /api/v1/teachers`：分页、`search`、`initial`、`college` 筛选。
- `GET /api/v1/teachers/{id}`：教师公开详情。
- `GET /api/v1/teachers/{id}/courses`：该教师的授课班级列表。
- `GET /api/v1/courses`：课程分页、`search`、`campus`、`weekday`、`college` 筛选。
- `GET /api/v1/courses/{id}`：教学班详情,含匹配到的 `teacher_id`。
- `GET /api/v1/meta`：数据版本、生成时间、记录数和课程数。
- `GET /api/v1/healthz`：健康检查。

导入开课信息(先导入教师快照,课程按教师姓名关联目录):

```bash
python3 scripts/xlsx_to_courses.py courses整理.xlsx courses.json
go run ./cmd/teach-api \
  -db ./data/teach.db \
  -import-courses courses.json \
  -import-only
```

公开读接口返回 `Cache-Control: public`、`ETag` 和 `Last-Modified`。数据快照重新导入后版本自动变化，浏览器和 CDN 会重新验证旧缓存。默认 CORS 为 `*`；生产环境请通过 `-cors-origins https://你的前端域名` 收紧来源。

## 活水评分数据同步

API 进程内置一个后台任务，把私有仓库 `swjtu-zip/huoshui-scraper`(`master` 分支 `docs/data/`,上游每天自动更新)的 `courses.json` / `reviews.json` / `stats.json` 镜像到本地目录，并写出 `meta.json`(同步时间、上游 commit、各文件 sha256)。这些 JSON 文件同时是数据库的导入来源和本地备份。

同步落盘后会自动导入 SQLite(`huoshui_courses` / `huoshui_reviews` / `huoshui_match` 三表):启动时若 `<-db 所在目录>/huoshui/{courses,reviews}.json` 存在即导入(幂等——dataset_meta 里记录的 sha256 与文件一致时跳过);之后每次同步有文件变化时自动重新导入并重建匹配。匹配规则:开课信息按规范化后的 (课程名, 教师)(去首尾空白、统一全半角、删除所有空格)对应到活水课程，一门课有多个活水候选时取评价数最多的一条。

前端不直接读这些 JSON，而是走 API:

- `GET /api/v1/huoshui/meta`：导入版本、课程/评价/匹配计数、院系列表。
- `GET /api/v1/huoshui/courses`:`search`(课程名或教师)、`dept`、`sort=reviews|rating|rate3`(评分排序只含评价数 ≥ 3 的课程)、`page`、`page_size`(默认 50，最大 100)。
- `GET /api/v1/huoshui/courses/{id}`：课程详情 + 按点赞数排序的前 100 条评价。
- `GET /api/v1/courses` 与 `GET /api/v1/courses/{id}` 的每个条目新增 `huoshui` 字段(无匹配时为 `null`):`objectId`、`reviewCount`、`rateOverall`、`rate1`/`rate2`/`rate3`、`attendanceOverall`、`examOverall`、`birdOverall`。

相关 flags 与配置：

- `-huoshui-sync`(默认 `true`)：开关后台同步。关闭时仍会在启动时导入已有 JSON 文件。
- `-huoshui-interval`(默认 `24h`)：两次同步的间隔；进程启动时会立即同步一次。
- `-huoshui-dir`(默认空)：数据目录，缺省为 `<-db 所在目录>/huoshui`，即 Docker 部署下的 `/data/huoshui/`。
- token 只从环境变量读取，依次尝试 `HUOSHUI_GITHUB_TOKEN`、`GH_TOKEN`、`GITHUB_TOKEN`,不提供 flag。上游是私有仓库,没有 token 时同步整体跳过(启动时打印一条 `huoshui sync disabled: no token` 警告),数据库里已有数据照常可用。

同步行为：每个文件先下载并校验 JSON 合法性，与本地副本做 sha256 比对，只有内容变化才原子替换(同目录临时文件 + rename,0644);`meta.json` 每次同步都会重写(GitHub commits API 限流或失败时上游 commit 字段写为 `null`)。任何同步或导入错误只记日志,不影响 API 服务,等下一个周期重试;进程收到 SIGTERM/SIGINT 时同步随之停止。`-import-only` 模式不会启动同步。

## Docker

```bash
docker build -t teach-api:local .
docker network create teach-network
docker run --rm --name teach-api --network teach-network \
  -e HUOSHUI_GITHUB_TOKEN=<huoshui-scraper 读权限 token> \
  -v teach-api-state:/data \
  -v "$PWD/../teach-crawler/data:/snapshot:ro" \
  teach-api:local \
  -db /data/teach.db \
  -import /snapshot/teachers.json
```

镜像运行时为非 root 用户。上面的命令使用 Docker volume 保存 SQLite 状态；如果改为绑定宿主机目录，该目录需要对容器用户（默认 UID 1000）可写。也可以先用本地命令完成导入，再挂载已有数据库。

与 `teach-web` 联合部署时，API 容器只加入内部 Docker 网络，不发布宿主机端口；由前端 Nginx 把同一域名下的 `/api/` 转发到 `teach-api:8080`。
