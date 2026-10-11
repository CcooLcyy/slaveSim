# 逆变器预设配置

这些文件是 slaveSim 可直接加载的完整 JSON 配置，不是下位机 configPusher 的点表。按厂商、机型和协议版本选择文件后，无需逐个手工录入寄存器。

## 选择文件

|厂家/型号|配置|说明|
|---|---|---|
|奥太 HF/TL|`aotai-hf-tl-telemetry.json`|小功率遥测|
|奥太 TLD|`aotai-tld-telemetry.json`|小功率遥测|
|奥太 TLC 10-30kW|`aotai-tlc10-30-telemetry.json`|中功率遥测|
|奥太 ASP250/500/630|`aotai-asp250-telemetry.json` / `aotai-asp500-telemetry.json` / `aotai-asp630-telemetry.json`|按功率型号选择|
|奥太控制|`aotai-controls-common.json` / `aotai-controls-large.json`|独立控制测试，不能与遥测合并到同一从站|
|古瑞瓦特 TL-X / MIN|`growatt-tlx-min.json`|3000 段输入寄存器|
|古瑞瓦特 TL3-X / MID / MAC|`growatt-tl3x-mid-mac.json`|0 段输入寄存器|
|古瑞瓦特 TL3-X / MAX|`growatt-tl3x-max.json`|含 MAX 专用点|
|固德威 MT G2|`goodwe-mtg2.json`|768 段遥测|
|固德威 SMT 50/60kW|`goodwe-smt50-60k.json`|含 PV5/PV6 和 SMT 调度点|
|固德威 SDT G2|`goodwe-sdtg2.json`|544 段遥测及适用扩展点|
|固德威单相单路 MPPT|`goodwe-single-mppt.json`|DNS/XS 适用点子集|
|固德威单相双路 MPPT|`goodwe-single-dual-mppt.json`|MS/DNS 适用点子集，仅协议列出的 PV1/PV2|
|华为 V3.0|`huawei-v3.json`|两份文档共同逆变器数值点|
|华为 V3.0 行为模型|`huawei-v3-model.json`|同一份点表绑定设备模型，控制联动P/Q、电流、状态与电量，见[行为仿真说明](../../doc/华为逆变器行为仿真.md)|

详细来源、PDF 页码、倍率及未覆盖项目见 [奥太/古瑞瓦特](aotai-growatt.md)、[固德威](goodwe.md)、[华为](huawei.md)。未覆盖储能设备、电表、字符串身份信息、厂商自定义接口等完整行为。

## 使用

网页可直接添加：进入“RS485 配置”，在所需总线的“设备类型”下拉框选择预置并点击“添加设备”，检查站号和参数，再“保存并应用”。同一总线可以同时添加自定义 Modbus 设备和多台不同型号的逆变器，站号不能重复。华为行为预置绑定设备模型，其余点表预置仍用于点位与协议联调，覆盖边界见下文。奥太控制预置为独立控制测试设备，不能将其寄存器直接合并进遥测从站。

单个模板默认一条总线、站号 1，软链接 `/home/daniel/serial-sim/tty0`。启动模拟器进程时可直接指定：

```bash
./slavesim -config configs/inverters/goodwe-smt50-60k.json -http :16000
```

Docker/Compose 部署先备份现有 `conf/config.json`，再将选定 JSON 复制为 `conf/config.json`，修改站号和串口路径后重启模拟器容器。源镜像中的模板在 `/opt/slavesim/examples/inverters/`；二进制发布包包含 `configs/inverters/`。网页更新仅更换模拟器二进制，不会覆盖已部署点表，也不会自动把新模板复制进旧容器，旧部署可从发布包取得这些 JSON。

多厂家同时测试可用根目录 `config.inverters.example.json`：7 路总线分别为奥太 ASP500 遥测、古瑞瓦特 MIN、古瑞瓦特 MID/MAC、固德威 MT G2、固德威 SMT 50/60kW、固德威 SDT G2、华为 V3.0，串口软链接依次为 `tty0` 到 `tty6`，每路站号 1。它是独立示例，不会替换已有运行配置或原 `config.example.json`。

不同型号可以挂同一路总线，但应取各模板的 `slaves`，设置不同站号，再合并为一个 `buses` 对象；不可把多个型号的 `registers` 叠到同一站号。PTY 链接必须位于与下位机共享的串口目录，且两个容器共享 `/dev/pts`。

## 数值与覆盖边界

以下固定点位限制适用于未配置 `model` 的模板。`huawei-v3-model.json` 的原始点表保持不变，设备模型另按地址、类型、倍率及范围校验绑定；该模板中的已实现控制会联动遥测，未实现参数拒绝写入，详细覆盖及仿真假设见行为仿真说明。

- 地址是直接出现在 Modbus 报文中的地址，不加 40000、不减一。32 位点占两个地址。
- `source.value`、正弦和其他来源参数都是原始整数，模拟器不做倍率换算。例如 Gain=100 的频率 50Hz 应配置 raw=5000；下位机自行配置 scale=0.01。
- `tag`/`unit` 只展示；unit 使用原始计数单位，详细工程单位及倍率以厂商说明为准。
- 默认 `unmapped_policy=exception`，未配置地址返回 0x02；连续读取不能跨未配置地址。按各点或连续已配置区间建立读块；明确接受空洞补零时，可在自己的测试配置中改为 `zero`，但补零不代表协议中真实存在该点。
- 控制写入仅改变寄存器值，可读回，不联动功率、运行状态、告警或重启，不模拟厂商特定写值范围、访问权限和功能码限制。华为字序选择以及奥太同地址读写不同含义的限制见各自说明。
- 模板含固定模拟值及部分动态值，用于地址、类型、倍率、状态和读写格式联调，不保证独立信号之间的物理一致性。

配置与逐点读响应测试位于 `internal/sim/inverter_configs_test.go`；关键点另有独立报文字节及负数控制值写回断言。验证命令为 Linux 下的 `go test ./internal/config ./internal/sim`。发布工具测试会检查模板和说明均随二进制包发布。
