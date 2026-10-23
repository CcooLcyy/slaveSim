# GitLab 持续集成

当前 GitLab 流水线验证 Linux amd64。合并请求执行检查与二进制构建；受保护 `master` 的推送和手动流水线在镜像验证成功后，复用 GitHub 已有发布工具，将 x64 产物上传到原 R2 测试通道。ARM64 构建尚未接入；现有 GitHub 工作流保留。

## 与 GitHub 流程对应

| 阶段 | GitLab 作业 | 执行内容 |
| --- | --- | --- |
| 元数据 | `metadata` | 生成一次 CI 版本、完整提交号、UTC 构建时间和镜像标签，通过 dotenv 供构建复用 |
| 检查 | `test` | `go vet ./...`、`go test -race ./...`、全部 Node 测试、全部 Python 工具测试 |
| 二进制 | `build-binary` | 测试成功后构建 amd64 模拟器与 probe，保存构建产物 |
| 镜像 | `build-image` | 下载二进制作业产物，仅受保护 `master` 的推送和手动流水线构建 amd64 镜像，并验证镜像内的程序、管理进程与真实 PTY 收发 |
| 发布 | `publish-r2` | 校验构建身份与 SHA256，调用原 `release.py prepare/publish`，上传 x64 二进制包、完整镜像包和更新清单到 R2 |

两种构建作业使用同一份元数据，构建产物在作业页面保留 7 天。CI 版本沿用 `<VERSION>-ci.<UTC 时间>+sha.<7 位提交号>`；Docker 标签将 `+` 转为 `_`。

二进制作业保存 `dist/amd64/` 下的 `slavesim`、`probe`、`metadata.json` 与 `SHA256SUMS`。镜像作业继续保留这些文件，并增加 `slavesim-image.tar.gz`、`image-id.txt` 和 `smoke-runtime.log`，更新校验信息。

当前不处理 `v*` 标签和 stable 通道。GitLab 显式使用 `--architectures amd64` 发布；原 GitHub 不传此参数时，仍要求双架构齐全。

## R2 发布

沿用原来的 `mskdsp-update` 存储桶、`slavesim` 前缀和 `ci/latest.json` 地址：

`https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/ci/latest.json`

`publish-r2` 依赖本次流水线的元数据、测试、二进制和镜像作业全部成功。作业再次校验产物来源与校验和，先生成发布包，再上传并从公开地址核对资产 SHA256 和长度；全部校验通过后才更新 `latest.json`。同通道发布串行执行，上传中的作业不自动取消，并保留原有不可变版本与禁止版本倒退的检查。

凭据复用 GitLab 中受保护、掩码的 `R2_ACCOUNT_ID`、`R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY`。发布工具将后两项在子进程环境中映射到 AWS 标准变量；凭据不写入源码、命令参数或构建产物。发布作业使用现有包含 Python 3.11 和 AWS CLI 的 `mskdsp-platform-ci-tools:bookworm-v2` 镜像。

本次 `latest.json` 仅包含已验证的 `linux-amd64`，旧 ARM64 包及其版本说明继续保留，单架构清理仅删除旧 x64 资产。ARM64 设备暂不能从当前最新清单获得更新。其他项目和 stable 通道不参与清理。

## Runner 与工具镜像

复用现有标签为 `linux`、`amd64`、`docker` 的共享 Docker executor，无需另建 Runner。作业指定 `slavesim-ci-tools:bookworm-v1`，使用宿主机已准备的镜像；`if-not-present` 避免把本地工具镜像当成公共仓库镜像拉取。

工具镜像提供 Go 1.24、Node.js 22、Python 3、gcc 与 BuildKit 客户端，定义位于 `docker/gitlab-ci-tools/Dockerfile`，基础镜像按摘要固定。Go、Node 和 Python 测试均使用项目源码及标准工具，不需要安装 npm 或 Python 第三方包。

`go test -race` 需要 CGO 与 gcc，不能全局关闭 CGO。最终二进制构建单独设置 `CGO_ENABLED=0`。PTY 测试使用作业容器自己的 `/dev/ptmx` 和 `/dev/pts`，无需特权容器或共享宿主机 PTY。

Node 22 的测试子进程报告协议存在中文日志反序列化问题。CI 为每个前端测试文件分别运行 `node --test --experimental-test-isolation=none`，保留文件之间的进程隔离，直接输出测试结果，任意一个文件失败都会使作业失败。

## 镜像构建边界

镜像构建复用现有受 mTLS 保护的 BuildKit 服务，使用 slaveSim 独立的客户端证书。连接变量 `BUILDKIT_HOST`、`BUILDKIT_CA_B64`、`BUILDKIT_CERT_B64`、`BUILDKIT_KEY_B64` 仅向 `ci-build` 环境开放，并作为受保护变量配置；证书变量同时掩码。客户端证书有效期一年，需要到期前轮换。普通合并请求只执行检查与二进制构建，不进入该环境。

镜像内验证通过 BuildKit 的验证阶段执行，覆盖版本输出、管理进程启动、网页模块与真实 PTY 收发；用作业 ID 防止验证步骤命中上一次缓存。导出的运行镜像不包含验证脚本。共享 Runner 不需要 Docker socket，也不需要启用 privileged。镜像结果以压缩归档及镜像身份信息保存为作业产物，再由独立发布作业上传 R2，不自动部署到设备。

## 本地检查

在 Linux 开发环境执行与 CI 相同的检查：

```bash
go vet ./...
go test -race ./...
for file in test/*.test.mjs; do
  node --test --experimental-test-isolation=none "$file" || exit $?
done
python3 -m unittest discover -s test -p 'test_*.py' -v
```

本地测试只验证代码与发布规则；R2 发布状态以 GitLab 发布作业和公开清单、资产核验结果为准，不代表 ARM64 原生测试或现场设备联调已经完成。
