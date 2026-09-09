# teach-web

这是与 `teach-api` 分离部署的纯静态前端。首页展示教师目录，每位教师有可直接分享的 `/teachers/{id}` 主页，包含头像、联系方式、办公地点和公开资料栏目；`/courses` 提供开课信息浏览(搜索、校区/星期筛选、分页)，`/courses/{id}` 为课程详情页并链接回教师主页，教师主页也会列出其授课班级。

## 本地预览

API 默认运行在 `http://localhost:8080` 时，可以直接启动静态服务器：

```bash
cd teach-web
python3 -m http.server 8081
```

然后打开 <http://localhost:8081>。API 地址在 [index.html](index.html) 的 `meta[name="api-base"]` 中配置，也可以在部署时注入同名的 `window.TEACH_API_BASE` 全局变量。

## Docker

```bash
docker build -t teach-web:local .
docker run --rm -p 8081:8080 teach-web:local
```

前后端虽然是两个容器，但生产入口保持同一个域名和端口：前端 Nginx 将 `/api/` 反向代理到 Docker 网络内的 API 容器。构建时指定公开 API 路径和容器上游地址，例如：

```bash
docker build \
  --build-arg API_BASE=/api \
  --build-arg API_UPSTREAM=swjtu-teach-api:8080 \
  -t teach-web:remote .
```

缓存策略已经写入 [nginx.conf](nginx.conf)：入口 HTML 使用 `no-cache`，版本化的 JS/CSS 使用一年 `immutable` 缓存。升级静态资源时递增文件名版本，只需重新发布 HTML 即可让浏览器获得新资源。

## 活水评分

`/huoshui` 页面展示来自活水(app.huoshui.org)的课程评分与学生评价;开课信息列表/详情页也会在匹配到活水数据时显示评分徽标与评价。数据不是实时抓取的,也不由浏览器下载整份 JSON:teach-api 进程每天把私有仓库 `swjtu-zip/huoshui-scraper`(`docs/data/`)的 JSON 同步到自己的状态目录 `/data/huoshui/` 并导入 SQLite,前端只调用分页 API(`/api/v1/huoshui/meta`、`/api/v1/huoshui/courses`、`/api/v1/huoshui/courses/{id}`,以及 `/api/v1/courses*` 响应里的 `huoshui` 字段)。

teach-api 落盘的 JSON 文件是数据库的导入来源兼本地备份:

- `courses.json` — 课程评分(约 6500 门课)
- `reviews.json` — 学生评价(约 16000 条)
- `stats.json` — 院系/教师/时间统计
- `meta.json` — 同步时间、上游 commit、各文件 sha256

部署只需要 teach-api 带 token;teach-web 无需任何额外挂载:

```bash
# teach-api:需要上游私有仓库的读 token,同步产物落在数据目录的 huoshui/ 子目录
docker run -d --name teach-api --network teach-network \
  -e HUOSHUI_GITHUB_TOKEN=<有 huoshui-scraper 读权限的 token> \
  -v /srv/teach:/data \
  teach-api:local \
  -db /data/teach.db

docker run -d --name teach-web --network teach-network -p 8080:8080 teach-web:local
```

如果还想让 Nginx 直接以 `/data/huoshui/` 静态路径提供这些 JSON(调试/兜底),可以把 huoshui 子目录只读挂载进 teach-web:`-v /srv/teach/huoshui:/usr/share/nginx/html/data/huoshui:ro`(**不要**把整个数据卷挂到站点根路径下,否则 `teach.db` 会被公开访问;前端代码不依赖这条路径)。本仓库 `data/huoshui/` 中提交的数据是镜像内置的种子:把它拷到 teach-api 的 `huoshui/` 目录,即可在首次联网同步前完成数据库导入。

本地开发或手动播种可用 [scripts/update-huoshui.sh](scripts/update-huoshui.sh)(需要 `HUOSHUI_GITHUB_TOKEN` 环境变量或已登录的 gh CLI);它与 teach-api 同步器写出的 `meta.json` 格式一致:sha256 比对、变化才原子替换、每次重写 meta(GitHub API 限流时上游 commit 字段为 `null`)。
