# uPic Server · 自部署图床

为 [IXYTYXI/uPic](https://github.com/IXYTYXI/uPic) 的「自定义图床」提供服务端：上传图片、返回外链、浏览图库、复制 Markdown、删除图片，使用 Docker Compose 在 Linux 上部署。uPic 原项目是 macOS 客户端，本项目是独立编写的兼容服务，不是把 Swift 桌面应用搬进容器。

支持 **PNG / JPEG / GIF**，默认每张 10 MiB、总图片容量 10 GiB、单图最多 2000 万像素；GIF 最多 1000 帧，所有帧累计像素也受同一限额约束。上传、列表、删除都需要密钥；图片外链公开。网页支持选择、拖放、粘贴及批量上传。密钥仅保存在当前网页内存中，刷新需要重新输入。

## 在 Linux 上部署

要求 Docker Engine、Docker Compose v2.20+、Bash、OpenSSL、gzip。首次构建需要拉取 Go 基础镜像；启用 HTTPS 还需 Caddy 镜像。推荐至少 1 CPU、1 GiB 内存及足够的数据磁盘空间。

将**本目录完整内容**放到服务器，例如 `/opt/upic-server`。不要在原始 Swift uPic 仓库根目录直接运行这些命令。

```bash
cd /opt/upic-server
./scripts/init.sh --public-url https://images.example.com
./scripts/deploy.sh --https
```

将 `images.example.com` 换成你的真实域名；先将域名的 A/AAAA 记录指向服务器，并开放 80/TCP、443/TCP（可选 443/UDP）。若发布了 AAAA，IPv6 也必须实际可达。Caddy 使用 HTTP 验证自动申请与续期证书；已有 Nginx/Caddy 占用 80/443 时使用下方「已有反向代理」方式。部署脚本的健康成功表示应用容器就绪，**还必须检查真实公网 HTTPS**：

```bash
curl --fail https://images.example.com/readyz
python3 scripts/smoke-test.py --url https://images.example.com --token-file .secrets/upload_token
```

Smoke 脚本会上传两张自带的 1×1 PNG，检查鉴权、上传、外链、列表、删除，并清理自己创建的图片。生产验收需要 Python 3；服务运行不需要 Python。

初始化会生成 `.env` 和 `.secrets/upload_token`，重复执行保留已有文件，**传入新域名不会覆盖已有 `.env`**。上传密钥请在服务器本地打开密钥文件后填入网页或 uPic；不要将密钥提交到 Git、写入 URL、贴入日志或放入前端源码。`.secrets` 目录权限为 0700，文件为 0444，以兼容 Compose 文件型 secret 的容器读取；访问权限由私有父目录和服务器管理员控制。

### 已有反向代理

```bash
./scripts/init.sh --public-url https://images.example.com
./scripts/deploy.sh
```

应用默认只绑定宿主机 `127.0.0.1:8080`，把 HTTPS 反向代理指向它。Nginx 示例：

```nginx
location / {
    client_max_body_size 16m;
    proxy_read_timeout 120s;
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

证书和 `server_name` 使用现有网关配置。服务根据 `PUBLIC_BASE_URL` 生成外链，不信任客户端的 Host 或 X-Forwarded-* 头。网页应从该公开域名访问，否则浏览器写请求会被 Origin 校验拒绝。

### 本机试用

```bash
./scripts/init.sh
./scripts/deploy.sh
# 浏览器访问 http://localhost:8080
```

此时 `PUBLIC_BASE_URL=http://localhost:8080`，外链仅适用于本机。远程服务器试用可以通过 `ssh -L 8080:127.0.0.1:8080 user@server` 转发。对外使用前改成实际 HTTPS origin，不能包含路径、查询或账号密码。

## 配置 uPic

uPic → 偏好设置 → 图床 → 添加「自定义」。

| 设置 | 值 |
| --- | --- |
| API 地址 | `https://images.example.com/api/upload` |
| 请求方式 | `POST` |
| 文件字段名 | `file` |
| 请求头 Authorization | `Bearer 你的上传密钥` |
| 请求头 Content-Type | `multipart/form-data` |
| URL 路径 | `["data", "url"]` |
| 域名 | **留空**，返回值已经是完整 URL |
| 保存路径 | 保持默认；服务端生成独立随机名称 |

上游 [CustomUploader.swift](https://github.com/IXYTYXI/uPic/blob/master/uPic/Models/Custom/CustomUploader.swift) 按 Content-Type 在表单与 JSON Base64 之间切换，本服务兼容两种请求。浏览器或 curl 使用 multipart 时应由客户端自动生成 boundary；不要为 curl 手写 Content-Type。上游 [配置教程](https://blog.svend.cc/upic/tutorials/custom/) 说明了 URL 路径数组语法。

## API

| 方法 | 路径 | 认证 | 用途 |
| --- | --- | --- | --- |
| POST | `/api/upload` | Bearer | 单张 multipart 或 JSON Base64 上传 |
| GET | `/api/images?offset=0&limit=24` | Bearer | 图库分页、总张数与容量 |
| DELETE | `/api/images/{id}` | Bearer | 删除指定图片 |
| GET / HEAD | `/i/{id}.{ext}` | 无 | 公共图片外链 |
| GET | `/api/config` | 无 | 上传大小上限和支持格式 |
| GET | `/healthz` | 无 | 进程存活 |
| GET | `/readyz` | 无 | 数据目录可写 |

上传成功返回 HTTP 201：

```json
{
  "success": true,
  "data": {
    "id": "服务端随机标识",
    "filename": "example.png",
    "url": "https://images.example.com/i/服务端随机标识.png",
    "mime": "image/png",
    "size": 12345,
    "width": 800,
    "height": 600,
    "created_at": "2026-10-13T08:00:00Z"
  }
}
```

JSON 上传体为 `{"file":"BASE64内容","filename":"example.png"}`。错误返回 `{"success":false,"error":{"code":"...","message":"..."}}`，并设置对应 HTTP 状态码。客户端用 401 判断密钥问题，413 判断超出限制，429 判断上传并发繁忙；不要对鉴权失败无限重试。批量上传由客户端逐张提交。

## 数据、升级与回滚

图片及元数据保存在名为 `upic_images` 的 Docker 持久卷，可通过 `.env` 的 `DATA_VOLUME_NAME` 修改。**不要执行 `docker compose down -v`，不要删除数据卷**。不同实例必须使用不同的 Compose 项目名、卷名与监听端口。本服务限定一个进程独占数据目录，不支持横向多副本。

原始名称仅作展示，存储路径由随机 ID 决定。文件与元数据在临时目录准备后原子发布，启动时恢复索引；格式和像素经过解析校验，拒绝 SVG/HTML。数据目录中的异常文件可能使启动失败，应先备份和排查，不要手工删文件来跳过检查。

更新项目源码后运行：

```bash
./scripts/deploy.sh
# 已启用的 HTTPS 模式会保留。
```

需要回退到上一次成功版本时，再运行：

```bash
./scripts/rollback.sh
```

部署脚本构建新的版本标签，保留当前镜像，替换应用并等待健康；失败时尝试恢复旧镜像，**即使恢复成功本次部署仍返回非零**。首次部署没有旧镜像可恢复。构建失败不会替换原容器。此为单实例更新，有短暂停机，不承诺零停机。

`rollback.sh` 只回退应用镜像，保留图片、当前 `.env` 和密钥；不回滚配置、Caddy 镜像或数据结构。修改配置后要自行恢复对应配置。镜像记录位于 `.deploy/current-image` 与 `.deploy/previous-image`，不要清理尚需回滚的 Docker 镜像。自建 CI 推送镜像后可以执行 `./scripts/deploy.sh --image registry.example.com/team/upic:版本`；服务器先自行完成 registry 登录。

### 备份与恢复

```bash
./scripts/backup.sh
# 恢复到一个不存在的新卷；不会覆盖或切换当前线上数据。
./scripts/restore.sh backups/upic-具体时间.tar.gz upic_images_restored
```

备份会短暂停止应用，导出完整数据卷，随后恢复应用并等待健康。helper 镜像预先拉取，失败也会尝试恢复服务；备份保存到权限受限的 `backups/`。备份内容不包含 `.env`、上传密钥和 Caddy 证书卷，请单独保护这些配置，并将备份复制到另一台机器或离线存储。备份不是磁盘冗余。

恢复只接受**可信的本服务备份**，并要求新卷不存在；失败后的新卷保留供排查。完成后将 `.env` 的 `DATA_VOLUME_NAME` 改为新卷，然后使用 `.deploy/current-image` 中的版本执行 `./scripts/deploy.sh --image 该版本`，再运行 smoke 验收。确认恢复前保留原卷。

`.deploy/lock` 防止同时执行发布、备份、恢复。正常退出自动释放；若主机断电或进程被 SIGKILL，先确认没有运维脚本仍在执行，再手工移除这个空锁目录。

## 环境参数

| 参数 | 默认值 / 约束 |
| --- | --- |
| `PUBLIC_BASE_URL` | 必填；例如 `https://images.example.com` |
| `SITE_ADDRESS` | Caddy 域名，需与公开 URL 对应 |
| `BIND_HOST` / `HTTP_PORT` | `127.0.0.1` / `8080` |
| `DATA_VOLUME_NAME` | `upic_images` |
| `UPLOAD_TOKEN_FILE` | 容器内 `/run/secrets/upload_token` |
| `UPLOAD_TOKEN` | 原生运行时可选，和 TOKEN_FILE 二选一，至少 32 位可打印 ASCII |
| `MAX_UPLOAD_BYTES` | `10485760`（10 MiB） |
| `MAX_STORAGE_BYTES` | `10737418240`（10 GiB），计图片原始字节，不含元数据和备份 |
| `MAX_IMAGE_PIXELS` | `20000000` |
| `MAX_CONCURRENT_UPLOADS` | `2` |
| `MAX_REQUEST_BYTES` | Caddy `16MB`，调大上传限额时也要调大，兼顾 Base64 约 4/3 膨胀 |
| `MEMORY_LIMIT` / `GOMEMLIMIT` | `512m` / `384MiB`，增大像素或并发需同时评估内存 |

图片链接是公开的，随机地址不等于私有访问控制。管理密钥为单管理员模型，持有者可操作全部图片。公共响应可缓存一小时，删除后的浏览器/CDN缓存可能持续到期；不要上传需要立即撤销访问的资料。首版没有 WebP/HEIC/SVG、缩略图转换、S3、多租户或审计系统。

## 在 Awesome Deploy 平台部署

仓库根目录的 `awesome.yaml` 是平台入口，使用 `Dockerfile.awesome` 离线编译 Go 服务，执行 `/upic-server serve`，监听 `8080`，通过 HTTP `/readyz` 检查数据目录可写，并把根路径路由到 `web`。独立 Compose 部署仍使用原来的 `Dockerfile`。

清单要求平台支持 `services.web.storage` 受控持久卷：`images` 挂载 `/data`，同一应用更新时复用，失败清理和版本退役不删除。原始阶段一平台不支持此能力，**必须先升级平台，不能把 DATA_DIR 改成 /tmp 来绕过检查**。平台更新采用先停止旧版、再启动新版的方式，避免两个进程同时写入图片目录；发布失败应恢复旧版。代码回退不能恢复已改变的数据内容或格式，仍需定期备份。

在平台服务器以服务账号设置上传密钥（交互输入，不会出现在仓库或命令参数）：

```bash
/opt/awesome-deploy/.venv/bin/awesome-deploy --config /etc/awesome-deploy/config.json secret-set \
  --repository https://github.com/IXYTYXI/pictest \
  --subdomain upic --name UPLOAD_TOKEN
```

管理页面提交仓库 `https://github.com/IXYTYXI/pictest.git`、ref `main`、域名前缀 `upic`。已有应用应使用原 `appId` 更新，并创建新的部署任务；重试此前失败任务的固定提交不会读到新 YAML。访问地址为 `https://upic.awesome.guanghexinzhi.cn`；更换前缀时必须同步修改 `app.subdomain`、`PUBLIC_BASE_URL` 和平台秘密配置作用域。

平台网关默认请求上限为 1 MiB，清单因此将单图限制为 **512 KiB**，为 multipart 和 Base64 留出余量；持久卷图片总容量仍为 10 GiB。如需恢复独立 Compose 的 10 MiB 单图上限，先由平台管理员调整该应用网关的请求限制，再修改清单。平台数据卷名由平台派生，不是 Compose 的 `upic_images`，不要对平台卷使用 Compose 的备份/恢复命令；应由平台运维在停止该应用后备份实际数据卷。

## 开发与验证

后端仅依赖 Go 标准库，静态页面内嵌进单个二进制，没有 npm/pip 数据库依赖。开发要求 Go 1.26+；操作脚本测试使用 Python 3。

```bash
go test -race -count=1 ./...
go vet ./...
node --check internal/gallery/web/app.js
python3 -m unittest discover -s tests -p 'test_*.py' -v
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/upic-linux-amd64 ./cmd/upic-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/upic-linux-arm64 ./cmd/upic-server
# 有 Docker daemon 时，使用已构建的镜像验收临时数据卷：
docker build -t upic-server:local .
python3 tests/docker_acceptance.py --image upic-server:local
```

GitHub Actions 定义了测试、两种 Linux 架构编译、镜像构建、Compose 启动和 HTTP 冒烟验收。当前交付的本地验证结果见 [verification.md](docs/verification.md)。没有目标服务器地址与域名，本次交付是代码和部署包，未宣称已上线。
