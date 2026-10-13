# 交付验证记录

验证日期：2026-10-13。环境：macOS 宿主机、Docker Desktop Linux ARM64（Docker Engine 29.7.2）。本记录描述本机可重复的验证，不表示已部署到公网。

## 已完成

| 验证 | 结果 |
| --- | --- |
| `go test -race -count=1 -json ./...` | 2 个包通过，96 个测试项（含子用例），0 失败 |
| `go vet ./...` | 通过 |
| Linux AMD64 / ARM64 静态二进制编译 | 两种架构均通过；运行容器验收使用 ARM64 |
| Python 运维脚本测试 | 12 项通过，覆盖失败回滚、初始化幂等、备份失败恢复、恢复不覆盖旧卷 |
| 每个 Shell 脚本的 `bash -n`、网页 `node --check` | 通过 |
| Compose 默认配置及 HTTPS profile 配置解析 | 通过 |
| 最终 Docker 镜像构建 | 通过，Go 构建镜像固定 digest，运行镜像基于 scratch |
| 真实 Docker 部署验收 | 部署健康、上传、容器重启、停机备份、新卷恢复、镜像回滚及 API 冒烟全部通过 |
| Caddy 配置验证 | 官方 Caddy 镜像验证通过；未申请真实域名证书 |
| 网页实际交互 | 密钥连接、选择图片上传、图库显示、复制操作提示、删除确认取消、断开会话通过；控制台无错误 |
| 独立代码审查 | 后端与运维复核完成，发现的持久化、边界校验、回滚基线及备份恢复问题已修复并回归 |

后端用例覆盖 multipart 与 JSON Base64 上传、认证、来源校验、大小/像素/GIF 累计帧限额、容量竞争、文件路径隔离、持久化及删除失败处理。真实容器验收对比了重启和恢复前后的图片字节，并验证镜像回滚不删除恢复数据。临时验收容器及数据卷在结束后清理。

## 复现

```bash
go test -race -count=1 ./...
go vet ./...
python3 -m unittest discover -s tests -p 'test_*.py' -v
node --check internal/gallery/web/app.js
for script in scripts/*.sh; do bash -n "$script"; done
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/upic-linux-amd64 ./cmd/upic-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/upic-linux-arm64 ./cmd/upic-server
docker build -t upic-server:local .
python3 tests/docker_acceptance.py --image upic-server:local
```

容器验收脚本创建隔离的 Compose 项目和随机命名临时卷，不需要生产上传密钥。完整结果日志与网页截图随本次工作区交付记录保存，源码包提供可重复执行的测试程序。

## 尚未验证的生产条件

没有提供目标 Linux 服务器地址及域名，本次未远程上线、未签发公网 TLS 证书，GitHub Actions 工作流也尚未在远端仓库触发。上线后应使用 README 中的公网 `/readyz` 和冒烟命令，验证 DNS、TLS、反向代理和实际磁盘权限。Linux AMD64 已编译但未在 AMD64 宿主机执行容器验收。

当前服务为单管理员、单实例、公共图片链接模型；支持 PNG/JPEG/GIF，不包含 S3、多租户或 WebP/HEIC。删除不能立即清除已有客户端/CDN 缓存。容量限制只计算图片原始字节，管理员需监控元数据、日志、备份和磁盘剩余空间。
