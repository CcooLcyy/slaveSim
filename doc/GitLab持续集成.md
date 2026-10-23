# GitLab 持续集成

当前 GitLab 流水线先验证 Linux amd64。推送 `master`、合并请求以及在 `master` 手动运行流水线会执行检查和构建。ARM64 与 R2 发布留待后续接入；现有 GitHub 工作流保留。

## 与 GitHub 流程对应

| 阶段 | GitLab 作业 | 执行内容 |
| --- | --- | --- |
| 元数据 | `metadata` | 生成一次 CI 版本、完整提交号、UTC 构建时间和镜像标签，通过 dotenv 供构建复用 |
| 检查 | `test` | `go vet ./...`、`go test -race ./...`、全部 Node 测试、全部 Python 工具测试 |
| 二进制 | `build-binary` | 测试成功后构建 amd64 模拟器与 probe，保存构建产物 |
| 镜像 | `build-image` | 下载二进制作业产物，仅受保护 `master` 的推送和手动流水线构建 amd64 镜像，并验证镜像内的程序、管理进程与真实 PTY 收发 |

两种构建作业使用同一份元数据，构建产物在作业页面保留 7 天。CI 版本沿用 `<VERSION>-ci.<UTC 时间>+sha.<7 位提交号>`；Docker 标签将 `+` 转为 `_`。

二进制作业保存 `dist/amd64/` 下的 `slavesim`、`probe`、`metadata.json` 与 `SHA256SUMS`。镜像作业继续保留这些文件，并增加 `slavesim-image.tar.gz`、`image-id.txt` 和 `smoke-runtime.log`，更新校验信息。

当前不处理 `v*` 标签发布，也不调用 `scripts/release.py publish`。现有发布脚本要求同时提供 amd64 与 arm64；单架构 CI 不生成或覆盖设备使用的 R2 更新清单。

## Runner 与工具镜像

复用现有标签为 `linux`、`amd64`、`docker` 的共享 Docker executor，无需另建 Runner。作业指定 `slavesim-ci-tools:bookworm-v1`，使用宿主机已准备的镜像；`if-not-present` 避免把本地工具镜像当成公共仓库镜像拉取。

工具镜像提供 Go 1.24、Node.js 22、Python 3、gcc 与 BuildKit 客户端，定义位于 `docker/gitlab-ci-tools/Dockerfile`，基础镜像按摘要固定。Go、Node 和 Python 测试均使用项目源码及标准工具，不需要安装 npm 或 Python 第三方包。

`go test -race` 需要 CGO 与 gcc，不能全局关闭 CGO。最终二进制构建单独设置 `CGO_ENABLED=0`。PTY 测试使用作业容器自己的 `/dev/ptmx` 和 `/dev/pts`，无需特权容器或共享宿主机 PTY。

## 镜像构建边界

镜像构建复用现有受 mTLS 保护的 BuildKit 服务，使用 slaveSim 独立的客户端证书。连接变量 `BUILDKIT_HOST`、`BUILDKIT_CA_B64`、`BUILDKIT_CERT_B64`、`BUILDKIT_KEY_B64` 仅向 `ci-build` 环境开放，并作为受保护变量配置；证书变量同时掩码。客户端证书有效期一年，需要到期前轮换。普通合并请求只执行检查与二进制构建，不进入该环境。

镜像内验证通过 BuildKit 的验证阶段执行，覆盖版本输出、管理进程启动、网页模块与真实 PTY 收发；用作业 ID 防止验证步骤命中上一次缓存。导出的运行镜像不包含验证脚本。共享 Runner 不需要 Docker socket，也不需要启用 privileged。镜像结果以压缩归档及镜像身份信息保存为作业产物，不部署到设备、不发布 R2。

## 本地检查

在 Linux 开发环境执行与 CI 相同的检查：

```bash
go vet ./...
go test -race ./...
node --test test/*.test.mjs
python3 -m unittest discover -s test -p 'test_*.py' -v
```

检查结果仅表示当前 amd64 CI 范围通过；不代表 ARM64 原生测试、现场设备联调或 R2 发布已经完成。
