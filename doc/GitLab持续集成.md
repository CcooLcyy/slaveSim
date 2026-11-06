# GitLab 持续集成

`.gitlab-ci.yml` 复用现有 x64 Runner，构建 Linux amd64、arm64，并在两个架构的镜像内完成运行验证后，统一发布到原 R2 测试通道。ARM64 运行验证使用 QEMU，不代表 ARM64 原生测试；stable 自动发布与设备部署不在本流程范围内。

## 双架构方案与验收

- 目标：同一流水线使用一份版本、提交与构建时间，交付 amd64、arm64 的二进制包和镜像包；每个架构分别保存产物与日志。
- 构建：`build-binary` 与 `build-image` 使用 `TARGETARCH=amd64/arm64` 矩阵。Runner 仍为 x64，Go 以 `CGO_ENABLED=0` 交叉编译；二进制阶段用 `go version -m` 跨架构读取构建信息，核对 `GOOS=linux`、目标 `GOARCH` 与 `CGO_ENABLED=0`，不直接运行目标程序。
- 检查：通用 Go、Node 和 Python 测试仍在 amd64 上执行。CI 临时 Dockerfile 直接复制 `dist/${TARGETARCH}` 中已构建的主程序与 probe，不另行编译；对应架构镜像内精确检查版本、完整提交、构建时间与运行架构，并验证管理进程、网页模块与两站真实 PTY 收发。因此镜像内验证和最终二进制发布使用同一份程序。ARM64 通过现有 BuildKit 的 QEMU 执行完整子进程链，验证失败不能跳过后发布。
- 发布：仅受保护 `master` 的推送与手动流水线允许发布。两个架构构建与运行验证全部成功、元数据及校验和一致后，单个 `publish-r2` 作业调用发布工具，一次性写入双架构清单，禁止各架构分别覆盖 `latest.json`。
- 验收：缺失或篡改任一架构输入、版本不符、旧验证标记、任一架构构建或运行失败均阻止发布。公开清单须同时包含同一版本、同一提交的 `linux-amd64`、`linux-arm64`，四个交付包均通过大小和 SHA256 核验后才更新入口。
- 边界：保持现有 R2 桶、前缀、CI 通道和设备按自身架构选择资产的规则；不部署或重启设备，不启用设备更新定时器。ARM64 真机验收独立进行。

2026-10-28 使用现有 CI 工具镜像和 BuildKit 完成预检：两个架构均成功编译、导出镜像，并通过版本身份、管理进程、网页模块和两站真实 PTY 收发验证；ARM64 在 QEMU 下执行。GitLab 流水线模拟校验通过，生成七个任务。预检未安装组件或修改服务器配置；正式发布结果以推送后的流水线与公开资产核验为准。

## 流水线阶段

| 阶段 | GitLab 作业 | 执行内容 |
| --- | --- | --- |
| 元数据 | `metadata` | 生成一次 CI 版本、完整提交号、UTC 构建时间和镜像标签，通过 dotenv 供构建复用 |
| 检查 | `test` | `go vet ./...`、`go test -race ./...`、全部 Node 测试、全部 Python 工具测试 |
| 二进制 | `build-binary` | 测试成功后以 amd64、arm64 矩阵构建模拟器与 probe，读取构建信息核对目标平台，按架构保存构建产物 |
| 镜像 | `build-image` | 下载并直接复制二进制作业产物，仅受保护 `master` 的推送和手动流水线以双架构矩阵构建镜像，并分别验证版本身份、运行架构、管理进程与真实 PTY 收发 |
| 发布 | `publish-r2` | 校验两架构构建身份与 SHA256，单次调用 `release.py prepare/publish --architectures amd64 arm64`，统一上传两架构二进制包、完整镜像包和更新清单到 R2 |

两种构建作业使用同一份元数据，构建产物在作业页面保留 7 天。CI 版本沿用 `<VERSION>-ci.<北京时间>+sha.<7 位提交号>`；Docker 标签将 `+` 转为 `_`。

二进制作业分别保存 `dist/amd64/`、`dist/arm64/` 下的 `slavesim`、`probe`、`metadata.json` 与 `SHA256SUMS`。镜像作业继续保留对应架构的这些文件，并增加 `slavesim-image.tar.gz`、`image-id.txt` 和 `smoke-runtime.log`，更新校验信息。

当前不处理 `v*` 标签和 stable 通道。GitLab 显式选择 amd64、arm64 一起发布；手动调用 `scripts/release.py prepare/publish` 不传架构参数时同样默认要求双架构齐全，显式选择单架构的能力继续保留。

## R2 发布

沿用原来的 `mskdsp-update` 存储桶、`slavesim` 前缀和 `ci/latest.json` 地址：

`https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/ci/latest.json`

`publish-r2` 依赖本次流水线的元数据、测试、二进制和镜像作业全部成功。作业再次校验产物来源与校验和，先生成发布包，再上传并从公开地址核对资产 SHA256 和长度；全部校验通过后才更新 `latest.json`。同通道发布串行执行，上传中的作业不自动取消，并保留原有不可变版本与禁止版本倒退的检查。

凭据复用 GitLab 中受保护、掩码的 `R2_ACCOUNT_ID`、`R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY`。发布工具将后两项在子进程环境中映射到 AWS 标准变量；凭据不写入源码、命令参数或构建产物。发布作业使用现有包含 Python 3.11 和 AWS CLI 的 `mskdsp-platform-ci-tools:bookworm-v2` 镜像。

双架构发布的 `latest.json` 同时包含本次已验证的 `linux-amd64` 与 `linux-arm64`，设备按自身架构选择资产。发布成功后执行双架构清理，只保留当前 CI 版本的四个包、版本清单、摘要及入口，共七个对象；旧 ARM64 资产也会按此规则清理。其他项目和 stable 通道不参与清理。

## Runner 与工具镜像

复用现有标签为 `linux`、`amd64`、`docker` 的共享 Docker executor，无需另建 Runner。作业指定 `slavesim-ci-tools:bookworm-v1`，使用宿主机已准备的镜像；`if-not-present` 避免把本地工具镜像当成公共仓库镜像拉取。

工具镜像提供 Go 1.24、Node.js 22、Python 3、gcc 与 BuildKit 客户端，定义位于 `docker/gitlab-ci-tools/Dockerfile`，基础镜像按摘要固定。Go、Node 和 Python 测试均使用项目源码及标准工具，不需要安装 npm 或 Python 第三方包。

`go test -race` 在 amd64 作业容器中运行，需要 CGO 与 gcc，不能全局关闭 CGO。最终两架构二进制构建单独设置 `CGO_ENABLED=0`。PTY 测试使用作业容器自己的 `/dev/ptmx` 和 `/dev/pts`，无需特权容器或共享宿主机 PTY。

Node 22 的测试子进程报告协议存在中文日志反序列化问题。CI 为每个前端测试文件分别运行 `node --test --experimental-test-isolation=none`，保留文件之间的进程隔离，直接输出测试结果，任意一个文件失败都会使作业失败。

## 镜像构建边界

镜像构建复用现有受 mTLS 保护的 BuildKit 服务，使用 slaveSim 独立的客户端证书。连接变量 `BUILDKIT_HOST`、`BUILDKIT_CA_B64`、`BUILDKIT_CERT_B64`、`BUILDKIT_KEY_B64` 仅向 `ci-build` 环境开放，并作为受保护变量配置；证书变量同时掩码。客户端证书有效期一年，需要到期前轮换。普通合并请求只执行检查与二进制构建，不进入该环境。

本地手动镜像构建仍使用仓库 `Dockerfile` 的 Go 构建阶段；CI 在临时 Dockerfile 中改为从 `dist/${TARGETARCH}` 复制已构建的 `slavesim` 和 `probe`，继续使用生产运行层的配置与文件。Go 的 `-trimpath` 产物构建信息可能不包含 `-ldflags`，因此二进制阶段只检查可读取的平台信息，版本身份以镜像内实际执行 `-version` 的精确结果为准，不能用缺失的 ldflags 记录推断版本校验成功。

镜像内验证通过 BuildKit 的验证阶段执行，覆盖对应架构程序的版本输出、管理进程启动、网页模块与真实 PTY 收发；用作业 ID 防止验证步骤命中上一次缓存。ARM64 在 x64 BuildKit 中通过 QEMU 运行，不在 Runner 内直接执行 ARM64 程序；BuildKit 必须支持管理进程、模拟器与探针的完整子进程链。导出的运行镜像不包含验证脚本。共享 Runner 不需要 Docker socket，也不需要启用 privileged。镜像结果以各架构压缩归档及镜像身份信息保存为作业产物，再由独立发布作业上传 R2，不自动部署到设备。

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

CI 版本与产物名称中的紧凑时间统一采用北京时间（UTC+8），清单 `published_at`/构建时间仍保存带 `Z` 的 UTC 时间。历史 UTC 编号不重命名；切换后新构建时间向前增加 8 小时，后续比较继续按编号时间排序。
