# 固德威 V1.10 预设点表

来源：用户提供的固德威并网协议客户版 V1.10（2025-05-09）。下表页码为 PDF 文件页序号，寄存器表为第 7-14 页，32 位高字在前定义在第 14 页，16 位高字节在前定义在第 5 页。

这里只覆盖主要数值遥测、状态、告警和控制；不是全协议或所有具体机型的功能仿真。站号统一设为 1（真机出厂默认 247），按联调需要修改。不同机型分别加载，不能把所有地址当作一台真实逆变器的通用点表。

数值是 raw，工程量 = raw / Gain。unit 显示的是每个原始计数对应的单位，不参与运算。默认为固定正常状态，告警位图可在运行页临时手动设置。保持区只配置一次：0x03 读取，readwrite 点可用 0x10 写入并读回。协议只支持 0x10 写入；模拟核心也会接受 0x06，WO 点在模拟器中可读回。

写命令只改变寄存器，不会联动功率、工作模式或触发重启；不会校验厂家特定写值范围。单相机型 256/257 真实行为是只写/不存储，本预设会保留写值，不能验证该特性。未配置地址返回 0x02；跨空洞批量读取应拆块。

未加入序列号/型号 STR、RTC、固件版本、韩国/台湾安规专用参数及点 267（仅台湾SMT）、880（日本）。单相双路模板含 PV1/PV2；MS 及其他 MPPT 通道数不同机型仅能测试本协议列出的通道，应按具体型号裁剪，不能据此认定已经覆盖所有通道。SDT G2 的 265、新遥测段及 SMT 新控制点须以文档第 3-4 页匹配软件版本为准。预设值仅用于格式和倍率联调，各点独立固定，不保证能量或功率守恒。


## MT G2：goodwe-mtg2.json（65 点）

|地址|点名|类型|Gain|原始初值|工程单位|权限|PDF页|
|---|---|---|---|---|---|---|---|

|0|起机电压|UINT16|10|600|V|readwrite|7|

|1|重连时间|UINT16|1|60|s|readwrite|7|

|2|市电电压下限|UINT16|10|1800|V|readwrite|8|

|3|市电电压上限|UINT16|10|2600|V|readwrite|8|

|4|市电频率下限|UINT16|100|4500|Hz|readwrite|8|

|5|市电频率上限|UINT16|100|5500|Hz|readwrite|8|

|256|有功调节量|UINT16|1|100|%|readwrite|8|

|257|PF无功调节量|UINT16|1|100|-|readwrite|8|

|258|无功调节量|INT32|1|0|var|readwrite|8|

|262|加减载幅度及指令ID|INT16|1|0|-|readwrite|8|

|265|无功百分比调节量|INT16|1|0|%|readwrite|8|

|288|开机命令|UINT16|1|0|-|readwrite|8|

|289|关机命令|UINT16|1|0|-|readwrite|8|

|290|重启命令|UINT16|1|0|-|readwrite|8|

|293|有功调节量|UINT16|10|1000|%|readwrite|9|

|312|阴影扫描开关|UINT16|1|0|-|readwrite|9|

|768|PV1电压|UINT16|10|6000|V|readonly|10|

|769|PV2电压|UINT16|10|6000|V|readonly|10|

|770|PV1电流|UINT16|10|150|A|readonly|10|

|771|PV2电流|UINT16|10|150|A|readonly|10|

|772|L1相电压|UINT16|10|2300|V|readonly|10|

|773|L2相电压|UINT16|10|2300|V|readonly|10|

|774|L3相电压|UINT16|10|2300|V|readonly|10|

|775|L1相电流|UINT16|10|800|A|readonly|10|

|776|L2相电流|UINT16|10|800|A|readonly|10|

|777|L3相电流|UINT16|10|800|A|readonly|10|

|778|L1频率|UINT16|100|5000|Hz|readonly|10|

|779|L2频率|UINT16|100|5000|Hz|readonly|10|

|780|L3频率|UINT16|100|5000|Hz|readonly|10|

|782|工作模式|UINT16|1|1|-|readonly|10|

|783|内部温度|UINT16|10|400|℃|readonly|10|

|784|错误代码位图|UINT32|1|0|-|readonly|10|

|786|累计发电量|UINT32|10|1000000|kWh|readonly|10|

|788|累计运行小时|UINT32|1|2000|h|readonly|10|

|791|警告代码|UINT16|1|0|-|readonly|10|

|793|功能状态位|UINT16|1|0|-|readonly|10|

|796|直流母线电压|UINT16|10|7000|V|readonly|10|

|797|直流母线中点电压|UINT16|10|3500|V|readonly|10|

|800|日发电量|UINT16|10|1800|kWh|readonly|11|

|850|当前输出功率|UINT32|1|55000|W|readonly|11|

|855|PV3电压|UINT16|10|6000|V|readonly|11|

|856|PV4电压|UINT16|10|6000|V|readonly|11|

|857|PV3电流|UINT16|10|150|A|readonly|11|

|858|PV4电流|UINT16|10|150|A|readonly|11|

|859|组串1电流|UINT16|10|75|A|readonly|11|

|860|组串2电流|UINT16|10|75|A|readonly|11|

|861|组串3电流|UINT16|10|75|A|readonly|11|

|862|组串4电流|UINT16|10|75|A|readonly|11|

|863|组串5电流|UINT16|10|75|A|readonly|11|

|864|组串6电流|UINT16|10|75|A|readonly|11|

|865|组串7电流|UINT16|10|75|A|readonly|12|

|866|组串8电流|UINT16|10|75|A|readonly|12|

|867|组串9电流|UINT16|10|75|A|readonly|12|

|868|组串10电流|UINT16|10|75|A|readonly|12|

|869|组串11电流|UINT16|10|75|A|readonly|12|

|870|组串12电流|UINT16|10|75|A|readonly|12|

|871|组串13电流|UINT16|10|75|A|readonly|12|

|872|组串14电流|UINT16|10|75|A|readonly|12|

|873|组串15电流|UINT16|10|75|A|readonly|12|

|874|组串16电流|UINT16|10|75|A|readonly|12|

|879|PID及防雷状态位|UINT16|1|7938|-|readonly|12|

|886|功率因数|UINT16|1000|1000|-|readonly|12|

|893|AC无功功率|INT32|1000|0|kvar|readonly|12|

|895|绝缘阻抗|UINT16|1|1000|kΩ|readonly|12|

|896|漏电流|UINT16|1|0|mA|readonly|12|


## SMT 50/60kW：goodwe-smt50-60k.json（80 点）

|地址|点名|类型|Gain|原始初值|工程单位|权限|PDF页|
|---|---|---|---|---|---|---|---|

|0|起机电压|UINT16|10|600|V|readwrite|7|

|1|重连时间|UINT16|1|60|s|readwrite|7|

|2|市电电压下限|UINT16|10|1800|V|readwrite|8|

|3|市电电压上限|UINT16|10|2600|V|readwrite|8|

|4|市电频率下限|UINT16|100|4500|Hz|readwrite|8|

|5|市电频率上限|UINT16|100|5500|Hz|readwrite|8|

|256|有功调节量|UINT16|1|100|%|readwrite|8|

|257|PF无功调节量|UINT16|1|100|-|readwrite|8|

|258|无功调节量|INT32|1|0|var|readwrite|8|

|262|加减载幅度及指令ID|INT16|1|0|-|readwrite|8|

|265|无功百分比调节量|INT16|1|0|%|readwrite|8|

|288|开机命令|UINT16|1|0|-|readwrite|8|

|289|关机命令|UINT16|1|0|-|readwrite|8|

|290|重启命令|UINT16|1|0|-|readwrite|8|

|291|并网功率限制开关|UINT16|1|0|-|readwrite|8|

|292|逆流上行功率百分比|UINT16|1|100|%|readwrite|9|

|312|阴影扫描开关|UINT16|1|0|-|readwrite|9|

|768|PV1电压|UINT16|10|6000|V|readonly|10|

|769|PV2电压|UINT16|10|6000|V|readonly|10|

|770|PV1电流|UINT16|10|150|A|readonly|10|

|771|PV2电流|UINT16|10|150|A|readonly|10|

|772|L1相电压|UINT16|10|2300|V|readonly|10|

|773|L2相电压|UINT16|10|2300|V|readonly|10|

|774|L3相电压|UINT16|10|2300|V|readonly|10|

|775|L1相电流|UINT16|10|800|A|readonly|10|

|776|L2相电流|UINT16|10|800|A|readonly|10|

|777|L3相电流|UINT16|10|800|A|readonly|10|

|778|L1频率|UINT16|100|5000|Hz|readonly|10|

|779|L2频率|UINT16|100|5000|Hz|readonly|10|

|780|L3频率|UINT16|100|5000|Hz|readonly|10|

|782|工作模式|UINT16|1|1|-|readonly|10|

|783|内部温度|UINT16|10|400|℃|readonly|10|

|784|错误代码位图|UINT32|1|0|-|readonly|10|

|786|累计发电量|UINT32|10|1000000|kWh|readonly|10|

|788|累计运行小时|UINT32|1|2000|h|readonly|10|

|791|警告代码|UINT16|1|0|-|readonly|10|

|793|功能状态位|UINT16|1|0|-|readonly|10|

|796|直流母线电压|UINT16|10|7000|V|readonly|10|

|797|直流母线中点电压|UINT16|10|3500|V|readonly|10|

|800|日发电量|UINT16|10|1800|kWh|readonly|11|

|804|PV5电压|UINT16|10|6000|V|readonly|11|

|805|PV5电流|UINT16|10|150|A|readonly|11|

|806|PV6电压|UINT16|10|6000|V|readonly|11|

|807|PV6电流|UINT16|10|150|A|readonly|11|

|814|功率因数|INT16|1000|1000|-|readonly|11|

|850|当前输出功率|UINT32|1|55000|W|readonly|11|

|855|PV3电压|UINT16|10|6000|V|readonly|11|

|856|PV4电压|UINT16|10|6000|V|readonly|11|

|857|PV3电流|UINT16|10|150|A|readonly|11|

|858|PV4电流|UINT16|10|150|A|readonly|11|

|859|组串1电流|UINT16|10|75|A|readonly|11|

|860|组串2电流|UINT16|10|75|A|readonly|11|

|861|组串3电流|UINT16|10|75|A|readonly|11|

|862|组串4电流|UINT16|10|75|A|readonly|11|

|863|组串5电流|UINT16|10|75|A|readonly|11|

|864|组串6电流|UINT16|10|75|A|readonly|11|

|865|组串7电流|UINT16|10|75|A|readonly|12|

|866|组串8电流|UINT16|10|75|A|readonly|12|

|867|组串9电流|UINT16|10|75|A|readonly|12|

|868|组串10电流|UINT16|10|75|A|readonly|12|

|869|组串11电流|UINT16|10|75|A|readonly|12|

|870|组串12电流|UINT16|10|75|A|readonly|12|

|871|组串13电流|UINT16|10|75|A|readonly|12|

|872|组串14电流|UINT16|10|75|A|readonly|12|

|873|组串15电流|UINT16|10|75|A|readonly|12|

|874|组串16电流|UINT16|10|75|A|readonly|12|

|879|PID及防雷状态位|UINT16|1|7938|-|readonly|12|

|893|AC无功功率|INT32|1000|0|kvar|readonly|12|

|895|绝缘阻抗|UINT16|1|1000|kΩ|readonly|12|

|896|漏电流|UINT16|1|0|mA|readonly|12|

|30115|AB线电压|UINT16|10|4000|V|readonly|13|

|30116|BC线电压|UINT16|10|4000|V|readonly|13|

|30117|CA线电压|UINT16|10|4000|V|readonly|13|

|30133|AC视在功率|UINT32|1000|55000|kVA|readonly|13|

|30137|输入总功率|INT32|1000|60000|kW|readonly|13|

|32086|转换效率|UINT16|100|9800|%|readonly|13|

|37207|运行标志7|UINT16|1|6|-|readonly|13|

|42404|有功功率固定值降额|INT32|1|55000|W|readwrite|14|

|42415|无功补偿Q/Qn|INT16|10|0|%Qn|readwrite|14|

|42439|功率调度禁能|UINT16|1|0|-|readwrite|14|


## SDT G2：goodwe-sdtg2.json（46 点）

|地址|点名|类型|Gain|原始初值|工程单位|权限|PDF页|
|---|---|---|---|---|---|---|---|

|0|起机电压|UINT16|10|600|V|readwrite|7|

|1|重连时间|UINT16|1|60|s|readwrite|7|

|2|市电电压下限|UINT16|10|1800|V|readwrite|8|

|3|市电电压上限|UINT16|10|2600|V|readwrite|8|

|4|市电频率下限|UINT16|100|4500|Hz|readwrite|8|

|5|市电频率上限|UINT16|100|5500|Hz|readwrite|8|

|256|有功调节量|UINT16|1|100|%|readwrite|8|

|257|PF无功调节量|UINT16|1|100|-|readwrite|8|

|258|无功调节量|INT32|1|0|var|readwrite|8|

|262|加减载幅度及指令ID|INT16|1|0|-|readwrite|8|

|265|无功百分比调节量|INT16|1|0|%|readwrite|8|

|288|开机命令|UINT16|1|0|-|readwrite|8|

|289|关机命令|UINT16|1|0|-|readwrite|8|

|290|重启命令|UINT16|1|0|-|readwrite|8|

|291|并网功率限制开关|UINT16|1|0|-|readwrite|8|

|292|逆流上行功率百分比|UINT16|1|100|%|readwrite|9|

|312|阴影扫描开关|UINT16|1|0|-|readwrite|9|

|544|错误代码位图|UINT32|1|0|-|readonly|9|

|546|累计发电量|UINT32|10|100000|kWh|readonly|9|

|548|累计运行小时|UINT32|1|2000|h|readonly|9|

|550|PV1电压|UINT16|10|6000|V|readonly|9|

|551|PV2电压|UINT16|10|6000|V|readonly|9|

|552|PV1电流|UINT16|10|100|A|readonly|9|

|553|PV2电流|UINT16|10|100|A|readonly|9|

|554|L1相电压|UINT16|10|2300|V|readonly|9|

|555|L2相电压|UINT16|10|2300|V|readonly|9|

|556|L3相电压|UINT16|10|2300|V|readonly|9|

|557|L1相电流|UINT16|10|160|A|readonly|9|

|558|L2相电流|UINT16|10|160|A|readonly|9|

|559|L3相电流|UINT16|10|160|A|readonly|9|

|560|L1频率|UINT16|100|5000|Hz|readonly|9|

|561|L2频率|UINT16|100|5000|Hz|readonly|9|

|562|L3频率|UINT16|100|5000|Hz|readonly|9|

|563|当前输出功率|UINT16|1|10000|W|readonly|9|

|564|工作模式|UINT16|1|1|-|readonly|9|

|565|内部温度|UINT16|10|400|℃|readonly|9|

|566|日发电量|UINT16|10|300|kWh|readonly|10|

|886|功率因数|UINT16|1000|1000|-|readonly|12|

|893|AC无功功率|INT32|1000|0|kvar|readonly|12|

|30115|AB线电压|UINT16|10|4000|V|readonly|13|

|30116|BC线电压|UINT16|10|4000|V|readonly|13|

|30117|CA线电压|UINT16|10|4000|V|readonly|13|

|30133|AC视在功率|UINT32|1000|55000|kVA|readonly|13|

|30137|输入总功率|INT32|1000|60000|kW|readonly|13|

|32086|转换效率|UINT16|100|9800|%|readonly|13|

|37207|运行标志7|UINT16|1|6|-|readonly|13|


## 单相单路MPPT（DNS/XS适用点子集）：goodwe-single-mppt.json（25 点）

|地址|点名|类型|Gain|原始初值|工程单位|权限|PDF页|
|---|---|---|---|---|---|---|---|

|0|起机电压|UINT16|10|600|V|readwrite|7|

|1|重连时间|UINT16|1|60|s|readwrite|7|

|2|市电电压下限|UINT16|10|1800|V|readwrite|8|

|3|市电电压上限|UINT16|10|2600|V|readwrite|8|

|4|市电频率下限|UINT16|100|4500|Hz|readwrite|8|

|5|市电频率上限|UINT16|100|5500|Hz|readwrite|8|

|256|有功调节量|UINT16|1|100|%|readwrite|8|

|257|PF无功调节量|UINT16|1|100|-|readwrite|8|

|262|加减载幅度及指令ID|INT16|1|0|-|readwrite|8|

|288|开机命令|UINT16|1|0|-|readwrite|8|

|289|关机命令|UINT16|1|0|-|readwrite|8|

|290|重启命令|UINT16|1|0|-|readwrite|8|

|312|阴影扫描开关|UINT16|1|0|-|readwrite|9|

|544|错误代码位图|UINT32|1|0|-|readonly|9|

|546|累计发电量|UINT32|10|100000|kWh|readonly|9|

|548|累计运行小时|UINT32|1|2000|h|readonly|9|

|550|PV1电压|UINT16|10|6000|V|readonly|9|

|552|PV1电流|UINT16|10|100|A|readonly|9|

|554|L1相电压|UINT16|10|2300|V|readonly|9|

|557|L1相电流|UINT16|10|160|A|readonly|9|

|560|L1频率|UINT16|100|5000|Hz|readonly|9|

|563|当前输出功率|UINT16|1|3000|W|readonly|9|

|564|工作模式|UINT16|1|1|-|readonly|9|

|565|内部温度|UINT16|10|400|℃|readonly|9|

|566|日发电量|UINT16|10|300|kWh|readonly|10|


## 单相双路MPPT（MS/DNS适用点子集）：goodwe-single-dual-mppt.json（27 点）

|地址|点名|类型|Gain|原始初值|工程单位|权限|PDF页|
|---|---|---|---|---|---|---|---|

|0|起机电压|UINT16|10|600|V|readwrite|7|

|1|重连时间|UINT16|1|60|s|readwrite|7|

|2|市电电压下限|UINT16|10|1800|V|readwrite|8|

|3|市电电压上限|UINT16|10|2600|V|readwrite|8|

|4|市电频率下限|UINT16|100|4500|Hz|readwrite|8|

|5|市电频率上限|UINT16|100|5500|Hz|readwrite|8|

|256|有功调节量|UINT16|1|100|%|readwrite|8|

|257|PF无功调节量|UINT16|1|100|-|readwrite|8|

|262|加减载幅度及指令ID|INT16|1|0|-|readwrite|8|

|288|开机命令|UINT16|1|0|-|readwrite|8|

|289|关机命令|UINT16|1|0|-|readwrite|8|

|290|重启命令|UINT16|1|0|-|readwrite|8|

|312|阴影扫描开关|UINT16|1|0|-|readwrite|9|

|544|错误代码位图|UINT32|1|0|-|readonly|9|

|546|累计发电量|UINT32|10|100000|kWh|readonly|9|

|548|累计运行小时|UINT32|1|2000|h|readonly|9|

|550|PV1电压|UINT16|10|6000|V|readonly|9|

|551|PV2电压|UINT16|10|6000|V|readonly|9|

|552|PV1电流|UINT16|10|100|A|readonly|9|

|553|PV2电流|UINT16|10|100|A|readonly|9|

|554|L1相电压|UINT16|10|2300|V|readonly|9|

|557|L1相电流|UINT16|10|160|A|readonly|9|

|560|L1频率|UINT16|100|5000|Hz|readonly|9|

|563|当前输出功率|UINT16|1|3000|W|readonly|9|

|564|工作模式|UINT16|1|1|-|readonly|9|

|565|内部温度|UINT16|10|400|℃|readonly|9|

|566|日发电量|UINT16|10|300|kWh|readonly|10|
