# uPic 兼容图床服务实施方案

目标：交付可在 Linux Docker Compose 自动部署的单实例图床，兼容 IXYTYXI/uPic 的自定义上传协议。用户已选择 Linux / Docker Compose。

## 架构和边界

Go 标准库 HTTP 服务内嵌网页，不依赖外部数据库或运行时软件包。上传文件和元数据保存在独立持久卷，Caddy 可选提供 HTTPS。公共图片可凭链接访问；上传、列表和删除统一 Bearer Token 鉴权。此服务为独立实现，不包含上游 Swift 源码。PNG、JPEG、GIF 为首版明确支持格式。

考虑了两种方案：本地持久卷最适合当前单服务器需求，选为实现；S3 对象存储适合扩容但引入外部配置和费用，留作后续。独立 Compose 方案已验收。用户随后要求接入 Awesome Deploy 并长期保存图片，因此补充根目录 awesome.yaml 与 Dockerfile.awesome，同时扩展平台的受控持久卷能力；原始无状态平台必须升级后才能运行此清单。

## 接口约定

- `POST /api/upload`，multipart 字段 `file`，同时兼容 uPic 默认 JSON Base64 `{"file":"..."}`，可选 `filename`。201 返回 `{"success":true,"data":Image}`；Image 包含 `id, filename, url, mime, size, width, height, created_at`。
- `GET /api/images?offset=0&limit=24` 返回 `{"success":true,"data":{"items":[Image],"total":0,"bytes":0,"max_bytes":10737418240}}`。
- `DELETE /api/images/{id}` 成功 200 `{"success":true}`。
- `GET /i/{id}.{ext}` 及 HEAD 为公共图片访问。上限、格式和并发限制生效于上传；不接受远程 URL 拉取。
- `GET /healthz` 存活，`GET /readyz` 存储就绪，`GET /api/config` 公共返回 `max_upload_bytes, formats`；不暴露 token。
- 所有错误格式 `{"success":false,"error":{"code":"...","message":"..."}}`。URL 来自经验证的 `PUBLIC_BASE_URL`，不信任 Host/转发头。
- 环境变量：`LISTEN_ADDR=:8080`、`PUBLIC_BASE_URL`、`DATA_DIR=/data`、`UPLOAD_TOKEN` 或 `UPLOAD_TOKEN_FILE`（二选一，至少32位）、`MAX_UPLOAD_BYTES=10485760`、`MAX_STORAGE_BYTES=10737418240`、`MAX_IMAGE_PIXELS=20000000`、`MAX_CONCURRENT_UPLOADS=2`。

## 实施和验收

- [x] 后端：协议、鉴权、格式/大小、重启持久化、路径穿越、删除、并发/容量测试，以及配置、文件存储、HTTP 和主进程实现。
- [x] 网页：中文响应式界面，内存保存 token，选择/拖拽/粘贴批量上传，图库、分页、复制 URL/Markdown、删除确认。无远程资源或 CDN。
- [x] 部署：多阶段静态镜像，非 root、只读根、资源限制；Compose 持久卷、健康检查、重启策略；幂等初始化、启动、健康验收、停机一致性备份、恢复到新卷、镜像回滚。
- [x] CI：Go test/race/vet、Linux amd64/arm64 编译、Docker 构建及 HTTP 冒烟配置；无生产凭证写入源码。
- [x] 综合验收：本地真实 HTTP 上传/访问/重启/删除；真实 Docker 备份、恢复、回滚；Compose 配置校验；独立代码审查；完整中文 README 与验证记录。

2026-10-13 已在 Docker Desktop 的 Linux ARM64 容器内完成真实部署验收；Linux AMD64 和 ARM64 均已交叉编译。具体证据和范围见 [verification.md](verification.md)。公网域名、证书签发和目标服务器环境仍需实际部署时验证。
