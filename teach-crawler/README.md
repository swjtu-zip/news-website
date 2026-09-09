# teach-crawler

西南交通大学教师目录与教师主页采集器。它抓取目录页上的头像、教师主页中的公开基本资料、联系方式、办公地点和详细栏目；课程、成绩等后续数据不进入本次教师快照。源站脚本加密的联系方式会通过源站接口解码，解码失败时不会把密文写入快照。

## 本地运行

Go 1.24+：

```bash
cd teach-crawler
GOTOOLCHAIN=local go test ./...
GOTOOLCHAIN=local go run ./cmd/teach-crawler \
  -output ./data/teachers.json
```

默认遍历 `a-z` 目录页，并抓取每个去重后的教师主页。第一次核对网络或只抓一个字母时可以使用：

```bash
go run ./cmd/teach-crawler -letters a -output ./data/teachers-a.json
```

常用参数：

- `-base-url`：默认为 `https://faculty.swjtu.edu.cn`，测试时可指向本地 HTTP 服务。
- `-letters`：`a-z` 或逗号分隔的字母，如 `a,c,d`。
- `-request-gap`：请求间隔，默认 `350ms`；请勿为了提速取消对源站的礼貌限速。
- `-workers`：教师主页并发数，默认 `4`。
- `-list-only`：只采集目录页，不请求教师主页。
- `-fail-on-profile-error`：任一主页失败时以非零状态退出。
- `-raw-dir`：可选原始 HTML 调试目录；原始主页可能含未整理的页面内容，只能放私有目录，不能作为公共静态资源。

输出是可供后续 `teach-api` 导入的 JSON 快照，包含每个列表页状态、目录条目数、去重数、主页成功/失败数和目录名与主页名的核对结果。写文件采用临时文件加原子替换，避免 API 读到半份快照。

## Docker

从仓库根目录构建：

```bash
docker build -t teach-crawler:local ./teach-crawler
mkdir -p "$PWD/teach-crawler/data"
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -v "$PWD/teach-crawler/data:/data" \
  teach-crawler:local \
  -raw-dir /data/raw
```

上面的 Docker 示例显式启用了原始 HTML 保存；该目录可能含联系方式，必须保持私有。镜像入口默认写入 `/data/teachers.json`，数据目录需要挂载到宿主机持久化。默认容器用户为 UID 1000；若宿主机目录不是该 UID 可写，请先调整目录权限，或运行时追加 `--user "$(id -u):$(id -g)"`。也可以使用 Docker named volume。构建阶段编译为静态二进制，运行阶段使用 `scratch`，容器不包含 Go 工具链。

## 数据核对

运行后重点检查 `data/teachers.json` 中的：

- `stats.list_pages` 是否等于选定字母数量，`list_pages_failed` 是否为 `0`；
- `directory_entries`、`unique_teachers` 和 `duplicate_directory_entries` 是否符合目录实际情况；
- `profiles_failed` 是否为 `0`，`name_mismatches` 是否为 `0`；
- 抽查 `teachers[].profile_url`、`name`、`college`、`position` 与源站主页。
