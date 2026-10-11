# slaveSim

电网终端设备仿真工具，当前优先提供华为逆变器行为模型，并保留通用 Modbus RTU 点位模拟。通过 PTY 创建虚拟串口设备，让下位机 `mskdsp` 以
`TRANSPORT_SERIAL` 方式直接连接，从而在没有真实设备的情况下复现现场环境。

设计与验收标准见 [`doc/设计方案.md`](doc/设计方案.md)。

华为设备行为仿真使用 [`configs/inverters/huawei-v3-model.json`](configs/inverters/huawei-v3-model.json)，不再独立造每个点的数值：功率命令会驱动实际P/Q、三相电流、模式反馈和电量积分。网页提供光照场景、控制入口和实际/目标功率曲线。首版范围、版本假设及启动模拟器进程的方法见 [`doc/华为逆变器行为仿真.md`](doc/华为逆变器行为仿真.md)。原 `huawei-v3.json` 仍是固定点位模板。

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

### 构建 arm64 镜像（部署到设备）

下位机设备是 arm64，通常需要在 x64 机器上交叉构建。运行阶段使用目标架构的 Python 镜像且不含
任何 `RUN` 指令，**整个过程不需要 qemu**，只是让 Go 换一个目标架构编译：

```bash
docker build --platform linux/arm64 -t slavesim:arm64 .
docker image inspect slavesim:arm64 --format '{{.Architecture}}'   # 应为 arm64

# 传到设备
docker save slavesim:arm64 | gzip > slavesim-arm64.tar.gz
scp slavesim-arm64.tar.gz <设备>:~/
# 设备上（docker 需要提权时加 sudo）
gunzip -c slavesim-arm64.tar.gz | sudo docker load
```

也可以直接用 CI 产出的 arm64 二进制（产物名 `slavesim-linux-arm64`），设备上无需 Docker：

```bash
./slavesim-linux-arm64 -config config.json -http :16000
```

### 持续集成

`.github/workflows/ci.yml` 在两个**原生** runner 上跑同一套 `go vet` / `go test` / `go build`：

| Job | Runner | 产出 |
| --- | --- | --- |
| `x64` | `ubuntu-latest` | `slavesim-linux-amd64` |
| `arm64` | `ubuntu-24.04-arm` | `slavesim-linux-arm64` |
| `docker-cross` | `ubuntu-latest` | 验证 `--platform linux/arm64` 交叉构建镜像 |

arm64 用的是 GitHub 托管的 arm64 runner，所以 `internal/sim/bus_test.go` 里那些
**走真实 PTY 的集成测试是在 arm64 上真跑**，而不是交叉编译完就不验证。

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

基础版本由根目录 `VERSION` 管理，当前为 `0.0.1`。本地普通构建显示 `0.0.1-dev`，CI 测试版采用上位机的编号风格：`0.0.1-ci.20261010t083000+sha.abcdef1`（时间为 UTC，提交号取前 7 位）。CI 与发布工作流都为两个架构注入同一份版本元数据。

推送 `master` 后，GitHub Actions 自动将双架构二进制包和 Docker 镜像包发布到 R2 的 `slavesim/ci` 通道；正式标签 `vX.Y.Z` 发布到 `slavesim/stable`。首次使用需要配置三个 R2 Actions Secrets。

正式标签须与该提交的 `VERSION` 一致；Docker 标签和资产路径把版本中的 `+` 转为 `_`。旧 CI 编号仍可读取，已部署设备须先单独迁移更新脚本与共享版本模块，详见 [版本编号与迁移](doc/版本编号方案.md)。

- [测试通道清单](https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/ci/latest.json)
- [稳定通道清单](https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/stable/latest.json)（首次正式发版后可用）

容器内的常驻管理进程提供“检查更新”和“更新到最新版”按钮。默认更新源为上述测试通道，可通过挂载 `updater.json` 的 `manifest_url` 配置。检查与安装均由用户手动触发；下载并校验二进制包后，仅重启模拟器子进程，容器和镜像保持不变。配置、日志与 `runtime/` 中的已安装版本持久保存，健康检查失败会回滚。容器重启后仍使用已安装版本。

首次部署由 `scripts/update.py init` 和 `apply --allow-initial-install` 安装基础镜像；进入网页更新模式后，旧的 Docker 镜像更新命令和定时器会拒绝运行。管理进程或基础镜像升级仍需单独维护。已有部署迁移需备份并重建一次容器，详见 [单容器网页更新](doc/单容器网页更新.md) 与 [R2 发布与自动更新](doc/R2发布与自动更新.md)。

## 与下位机对接

模拟器启动后会打印并建立软链接，例如：

```
[slaveSim] 虚拟串口已就绪: 软链接=/home/daniel/serial-sim/tty0 实际设备=/dev/pts/5 从站数=4
```

下位机侧配 `TRANSPORT_SERIAL`，`serial.device` 填**软链接路径**：

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
