# slaveSim

电网终端设备仿真工具，当前优先提供华为逆变器行为模型，并保留通用 Modbus RTU 点位模拟。通过 PTY 创建虚拟串口设备，让下位机 `mskdsp` 以
`TRANSPORT_SERIAL` 方式直接连接，从而在没有真实设备的情况下复现现场环境。

设计与验收标准见 [`doc/设计方案.md`](doc/设计方案.md)。

华为设备行为仿真使用 [`configs/inverters/huawei-v3-model.json`](configs/inverters/huawei-v3-model.json)，不再独立造每个点的数值：功率命令会驱动实际P/Q、三相电流、模式反馈和电量积分。网页提供光照场景、控制入口和实际/目标功率曲线。首版范围、版本假设及启动模拟器进程的方法见 [`doc/华为逆变器行为仿真.md`](doc/华为逆变器行为仿真.md)。原 `huawei-v3.json` 仍是固定点位模板。

网页新增设备支持两种方式：进入“RS485 配置”，在目标总线的“设备类型”中选“自定义 Modbus 设备”手工配置寄存器，或选择已有逆变器预置，点击“添加设备”。预置自动载入完整点表并分配未占用站号；可修改站号、响应延迟，华为行为预置还能调整环境参数。点击“保存并应用”后运行，新增设备不会替换已有从站或总线串口路径。各预置直接内嵌在程序中，无需手动下载 JSON；程序更新仍保留当前运行配置，由用户选择何时添加设备。

网页默认使用黑灰色的深色主题，也可在页头“外观”中选择浅色或跟随系统。选择保存在当前浏览器中，刷新后保留；选择“跟随系统”时会实时响应系统外观变化。主题切换覆盖运行工作区、RS485 配置和程序更新，不影响未保存草稿或设备运行。

## 它解决什么问题

下位机的 Modbus RTU 有两条下行链路：

| 传输类型 | 依赖 |
| --- | --- |
| `TRANSPORT_SERIAL` | 本地 `/dev/tty*` 设备 |
| `TRANSPORT_MQTT_UART` | 外部 `uartManager` 进程 + MQTT |

模拟环境里没有 `uartManager`，所以本工具只服务第一条：**用 PTY 造出 `/dev/pts/N`，
再挂一个稳定名字的软链接给下位机当串口用。**

## 快速开始

### 用 Docker（推荐，目标机上不需要 Go 工具链）

```bash
mkdir -p conf
cp config.example.json conf/config.json  # 仅首次初始化，已有配置不要覆盖
cp deploy/updater.runtime.example.json conf/updater.json  # 仅首次初始化，可修改更新源
docker build -t slavesim:latest .
docker run -d --name slavesim \
  -p 16000:16000 \
  --log-driver json-file --log-opt max-size=10m --log-opt max-file=3 \
  -v /dev/pts:/dev/pts \
  -v /home/daniel/serial-sim:/home/daniel/serial-sim \
  -v "$PWD/conf:/etc/slavesim" \
  -v "$PWD/runtime:/var/lib/slavesim" \
  --restart unless-stopped \
  slavesim:latest
```

或用 compose：

```bash
mkdir -p conf
cp config.example.json conf/config.json  # 仅首次初始化，已有配置不要覆盖
cp deploy/updater.runtime.example.json conf/updater.json  # 仅首次初始化，可修改更新源
docker compose up -d --build
```

配置目录需要对容器进程可写。Web 保存使用同目录临时文件和原子替换，单文件挂载即使改成可写也无法完成替换。挂载 `conf` 会遮蔽镜像中的默认更新源，需保留 `conf/updater.json` 才能启用网页更新。旧版本地 Compose 部署需把既有 `config.json` 复制到 `conf/config.json`，保留既有更新源与 `runtime/`，更新 Compose 后执行 `docker compose up -d --build --force-recreate` 重建模拟器容器。配置目录建议专用，挂载后其中的文件都会进入容器；由发布更新器管理的部署按 [R2 发布与自动更新](doc/R2发布与自动更新.md) 中的旧部署迁移步骤处理。

### 日志容量限制

启用 `-logfile` 时，默认每个文件最多 50 MiB，保留 5 个历史文件（`.1` 最新），正常日志总量约 300 MiB。可通过 `-log-max-size-mib` 和 `-log-backups` 调整，后者允许为 0；收发报文仍完整输出。已有超大日志在首次启动时轮转并保留，可能暂时超出预算，随后随正常轮转淘汰。详细行为和故障处理见 [日志保留策略](doc/日志保留策略.md)。

两份 Compose 和上面的 `docker run` 示例把 Docker 标准输出日志限制为每文件 10m、最多 3 个文件，约 30 MiB。Docker 日志参数及容器管理脚本的变更需要更新基础镜像、重建容器后生效；网页更新仅替换 Go 程序，不更新这两部分。已有 Compose 部署应用新版镜像与编排后需执行 `docker compose up -d --force-recreate`，本地构建部署加 `--build`。

管理进程在更新、回滚及子进程重启时沿用日志参数。默认参数不显式传给子进程，以允许回滚旧程序；尚未支持日志轮转的旧程序仍没有文件容量限制。自定义非默认值会透传给子进程，旧程序若不支持这些参数，将无法启动或回滚；回滚到旧程序前需恢复默认设置并重建容器。

### 构建 arm64 镜像（部署到设备）

下位机设备是 arm64，通常需要在 x64 机器上交叉构建。运行阶段使用目标架构的 Python 镜像且不含
任何 `RUN` 指令，**下面的镜像交叉构建不需要 QEMU**，只是让 Go 换一个目标架构编译。CI 还会执行镜像内运行验证，该步骤在 x64 环境中需要 QEMU：

```bash
docker build --platform linux/arm64 -t slavesim:arm64 .
docker image inspect slavesim:arm64 --format '{{.Architecture}}'   # 应为 arm64

# 传到设备
docker save slavesim:arm64 | gzip > slavesim-arm64.tar.gz
scp slavesim-arm64.tar.gz <设备>:~/
# 设备上（docker 需要提权时加 sudo）
gunzip -c slavesim-arm64.tar.gz | sudo docker load
```

也可以手动交叉编译 arm64 二进制，设备上无需 Docker。GitHub Actions 在各架构的原生 Runner 上构建和验证 amd64、arm64：

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o slavesim-linux-arm64 .
# 将二进制传到设备后运行
./slavesim-linux-arm64 -config config.json -http :16000
```

### 持续集成

[GitHub Actions 工作流](.github/workflows/ci.yml) 按以下顺序执行：

| Job | 触发范围 | 内容与产出 |
| --- | --- | --- |
| `checks` | 面向 master 的 PR、master 推送或手动运行 | 共享版本、提交与构建时间，运行全部前端与 Python 工具测试 |
| `build` | 通用检查通过后 | 在 amd64、arm64 Runner 上分别运行 Go 静态检查与 race 测试，构建程序和探针，验证镜像内的 PTY 收发并上传归档 |
| `publish` | 受保护 master 推送或手动运行 | 两架构均通过后，统一发布二进制包、镜像包与 R2 测试通道清单 |

两种架构的验证产物可从 Actions 下载。发布任务串行运行，校验来源提交仍为当前 master 后，使用本次运行的产物发布。

启动后访问 `http://<主机>:16000` 打开界面。

### 自带的主站探针

`cmd/probe` 是一个极简的 Modbus RTU 主站，走的是和下位机完全相同的路径
（打开串口设备、写请求、读响应），用于自检和排查：

```bash
go build -o probe_bin ./cmd/probe

# 读输入寄存器
./probe_bin -device /dev/pts/10 -id 1 -func 4 -addr 3004 -qty 2

# 写单寄存器
./probe_bin -device /dev/pts/10 -id 1 -func 6 -addr 40120 -write 1234
```

退出码：`0` 正常响应；`3` 读取超时（按真机行为本就不该响应）；
`4` 从站返回异常码。

> 注意：PTY 从端属主是创建它的进程（容器内是 root），权限为 `0600 root:tty`。
> 因此探针与下位机都需要以 root 身份、或在与模拟器同一容器命名空间内运行。
> 下位机容器使用 `--privileged`，满足该条件。

### 访问 WebUI

如果目标机没有把 16000 暴露到外部（例如只转发了 SSH 端口），用 SSH 隧道：

```bash
ssh -p 32118 -N -L 16000:127.0.0.1:16000 daniel@clsclear.top
# 然后浏览器打开 http://127.0.0.1:16000
```

### 本地构建

```bash
go build -o slavesim .
go test ./...
./slavesim -config config.example.json -http :16000
```

## R2 发布与自动更新

基础版本由根目录 `VERSION` 管理，当前为 `0.0.1`。本地普通构建显示 `0.0.1-dev`，CI 测试版采用 `0.0.1-ci.20261010t163000+sha.abcdef1` 格式，版本号使用北京时间，提交号取前 7 位，清单时间保留 UTC。Actions 各作业使用同一份版本元数据。

推送受保护 `master` 或在该分支手动运行 GitHub Actions 后，检查、两架构二进制构建和镜像验证均成功才统一发布到原 R2 `slavesim/ci` 通道。发布任务使用仓库的 `R2_ACCOUNT_ID`、`R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY`。发布脚本从公开地址核验资产后更新 `latest.json`，再清理旧 CI 版本资产。设备按清单中对应的架构获取更新。

当前标签不会触发 stable 发布。`scripts/release.py` 仍支持手动准备与发布，默认要求双架构，显式使用 `--architectures amd64` 可发布单架构。Docker 标签和资产路径把版本中的 `+` 转为 `_`。旧 CI 编号仍可读取，已部署设备须先单独迁移更新脚本与共享版本模块，详见 [版本编号与迁移](doc/版本编号方案.md)。

- [测试通道清单](https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/ci/latest.json)
- [稳定通道清单](https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/stable/latest.json)（由人工正式发布维护，当前流水线不更新）

容器内的常驻管理进程默认每60秒检查上述测试通道，发现新版本后自动下载、校验和安装，关闭网页后仍会执行。通过挂载 `updater.json` 的 `manifest_url` 配置更新源，`auto_update: false` 可关闭自动更新，`check_interval_seconds` 配置间隔（默认60秒）。网页展示自动更新状态及下次检查时间，仍保留“检查更新”和“更新到最新版”按钮；已有任务运行时不会并发重复安装，同版本不重启，失败后在下一轮检查重试。下载校验阶段原模拟器继续服务，安装时仅短暂重启模拟器子进程，容器和镜像保持不变。配置、日志与 `runtime/` 中的已安装版本持久保存，健康检查失败会回滚。容器重启后仍使用已安装版本。

管理进程启动后等待60秒首次检查，此后每60秒触发一轮；已有更新任务忙碌时跳过本轮，不排队并发，未配置有效更新源时自动停用。每分钟自动更新属于容器内Python管理进程的新能力，旧部署须先单独更新一次基础镜像并重建本项目容器，保留配置、日志和 `runtime/` 挂载；仅通过网页升级Go程序不会更新管理进程。新版管理进程读取旧的仅含 `manifest_url` 的配置时也默认启用60秒自动更新。后续自动更新只替换Go程序及配套探针，不自动升级基础镜像、Python管理进程或宿主部署工具。需要固定测试版本时，可先配置 `auto_update: false` 并重启管理进程。

首次部署由 `scripts/update.py init` 和 `apply --allow-initial-install` 安装基础镜像；进入网页更新模式后，旧的 Docker 镜像更新命令和定时器会拒绝运行。管理进程或基础镜像升级仍需单独维护。已有部署迁移需备份并重建一次容器，详见 [单容器网页更新](doc/单容器网页更新.md) 与 [R2 发布与自动更新](doc/R2发布与自动更新.md)。

## 与下位机对接

模拟器启动后会打印并建立软链接，例如：

```
[slaveSim] 虚拟串口已就绪: 软链接=/home/daniel/serial-sim/tty0 实际设备=/dev/pts/5 从站数=4
```

下位机侧配 `TRANSPORT_SERIAL`，`serial.device` 填**软链接路径**：

运行页优先展示当前实际设备路径，并提供复制按钮；下方的固定配置路径也可一键复制。实际 `/dev/pts/N` 编号由系统动态分配，模拟器或主机重启后可能变化。下位机应始终使用固定配置路径（例如 `/srv/serial-sim/tty5`）：只要总线配置中的 `link` 不变，模拟器重新启动会在同一路径建立指向当前 PTY 的软链接，无需每次修改下位机配置。

```jsonc
{
  "conn_name": "1-1#",
  "transport_type": "TRANSPORT_SERIAL",
  "serial": {
    "device": "/home/daniel/serial-sim/tty0",
    "baud_rate": 9600, "data_bits": 8,
    "parity": "PARITY_NONE", "stop_bits": "STOP_BITS_ONE"
  },
  "device_id": 1,
  "read_plan": { "mode": "EXPLICIT", "blocks": [
    { "function": "FUNCTION_READ_INPUT_REGISTERS", "start": 3004, "quantity": 2 }
  ]}
}
```

**关键前提**：下位机容器必须挂载同一份 `/dev/pts` 与同一个软链接目录，否则它看不到这个设备：

```bash
docker run ... -v /dev/pts:/dev/pts -v /home/daniel/serial-sim:/home/daniel/serial-sim ...
```

## 配置文件

配置为三层结构：**总线（一条虚拟串口）→ 从站（站号）→ 寄存器**。

已整理奥太、古瑞瓦特、固德威、华为的厂商点表，按机型选择 [`configs/inverters/`](configs/inverters/README.md) 下的完整 JSON，或用 [`config.inverters.example.json`](config.inverters.example.json) 同时模拟多家设备。预设的倍率、源协议页码、读写限制及未覆盖项见该目录说明。使用前调整站号和共享串口路径；已有部署的运行配置仍由用户选择并应用。

Web 页面可新增、编辑和删除多路 RS485 总线、从站及寄存器，支持固定、手动、正弦、斜坡和随机来源。点击“保存并应用”会校验完整配置、写入配置文件并更新正在运行的总线，重启后保留；单独在运行状态中手动设值是临时覆盖。每路总线的软链接应位于与下位机共享的串口目录内。接口及验收规则见 [Web 多路 RS485 配置](doc/Web多路485配置.md)。

配置页“删除全部总线”可一次清空全部总线、从站及点表草稿，只重绘一次，避免逐条删除时反复重建剩余配置控件。点击后须再“保存并应用”才影响运行设备及磁盘配置；未保存可“重新读取”恢复，已有零总线时按钮禁用。

支持保存零台模拟设备：可删除全部从站并保留空总线，也可删除全部总线，保存并应用后运行状态与磁盘配置均为零台，重启仍保持。保留空总线会保留原PTY及固定路径；删除总线会释放PTY和软链接。随后可通过页面从头添加逆变器。清空操作立即停止已删除设备的协议响应，已有下位机连接将收不到该设备数据；清空前应确认使用测试专用模拟器。

> **重要：`source.value` 是寄存器里的原始值，不是工程量。**
> 工程量由下位机用它自己的点表 `scale/offset` 换算（`value = raw * scale + offset`）。
> 例如下位机点表里 `scale = 1e-06`、希望看到 80kW，那么这里应当配 `80000000`。

支持的字段：

| 字段 | 取值 |
| --- | --- |
| `function` | `0x01` / `0x03` / `0x04` / `0x05` / `0x06` / `0x10` |
| `type` | `UINT16`（默认）/ `INT16` / `UINT32` / `INT32` / `BOOL` |
| `word_order` | `HL`（默认）/ `LH`，仅 32 位类型有效 |
| `byte_order` | `AB`（默认）/ `BA` |
| `bit_index` | 仅 `BOOL` 有效，默认 0 |
| `access` | `readonly`（默认）/ `readwrite` |
| `source.kind` | `const` / `manual` / `sine` / `ramp` / `random` |

地址空间按 Modbus 规范划分：`0x03` 与 `0x06`/`0x10` 共享**同一片**保持寄存器区，
所以写进去的值能被 `0x03` 读回来；`0x04` 输入寄存器与 `0x01`/`0x05` 线圈各自独立。

## 从站行为

默认对齐真实设备：

- 站号不是自己 → **静默不响应**
- CRC 校验失败 → **静默丢弃**
- 广播地址（`0x00` / `0xFF`）→ 执行写操作，但**不响应**
- 读未配置地址 → 返回异常码 `0x02`（`unmapped_policy` 设为 `zero` 可改为返回 0）
- 写只读寄存器 → 异常码 `0x02`
- 数量越界、写多寄存器字节数不符 → 异常码 `0x03`
- 不支持的功能码 → 异常码 `0x01`

## 目录结构

```
slaveSim/
├── main.go                     程序入口、日志输出、优雅退出
├── config.example.json         配置示例
├── Dockerfile                  多阶段构建，Python 管理进程与 Go 模拟器运行镜像
├── compose.yml                 部署编排（含必需的 -v /dev/pts）
├── doc/设计方案.md             设计与验收标准
└── internal/
    ├── config/                 配置结构与校验
    ├── modbus/                 CRC16、帧编解码、帧定界
    ├── pty/                    PTY 创建、raw 模式、稳定软链接
    ├── sim/                    寄存器、取值引擎、从站处理、总线读写循环
    ├── eventbus/               日志环形缓冲与订阅
    └── web/                    HTTP 接口与内嵌界面（SSE 推送日志）
```

## 尚未实现

见 [`doc/设计方案.md`](doc/设计方案.md) 第 10 节，按优先级：

1. 点表 CSV 导入与 tag 关联显示
2. 工程量与 raw 双列显示
3. `follow` 取值（写点跟踪 + 一阶惯性），用于 AGC 闭环联调
4. 帧间隔违规告警（锦浪协议要求查询 `>300ms`、控制 `>700ms`）
5. DLT645PCD 从站
6. 真实串口通道、MQTT 通道
