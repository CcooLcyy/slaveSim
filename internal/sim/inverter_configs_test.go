package sim

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/modbus"
)

// 验证协议中已核实的地址、类型及量纲对应原始字节，期望值独立于示例 JSON。
func TestInverterKnownProtocolValues(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		address  uint16
		quantity uint16
		dataType string
		want     []byte
		function byte
	}{
		// 华为协议 32080 为 I32，增益 1000 kW；50 kW 对应 raw 50000。
		{"华为有功功率50kW", "huawei-v3.json", 32080, 2, "INT32", []byte{0x00, 0x00, 0xC3, 0x50}, 0x03},
		// 华为协议 32085 为 U16，增益 100 Hz；50 Hz 对应 raw 5000。
		{"华为频率50Hz", "huawei-v3.json", 32085, 1, "UINT16", []byte{0x13, 0x88}, 0x03},
		// 华为协议 32087 为 I16，增益 10 摄氏度；35 摄氏度对应 raw 350。
		{"华为温度35摄氏度", "huawei-v3.json", 32087, 1, "INT16", []byte{0x01, 0x5E}, 0x03},
		// 华为协议 40120 为 U16，增益 10 kW；50 kW 限值对应 raw 500。
		{"华为有功限值50kW", "huawei-v3.json", 40120, 1, "UINT16", []byte{0x01, 0xF4}, 0x03},
		// 固德威 SMT 协议 850 为 U32、最小单位 1 W；55 kW 对应 raw 55000。
		{"固德威SMT有功功率55kW", "goodwe-smt50-60k.json", 850, 2, "UINT32", []byte{0x00, 0x00, 0xD6, 0xD8}, 0x03},
		// 固德威 SMT 协议 893 为 I32，增益 1000 kvar；默认无功为零。
		{"固德威SMT无功功率零", "goodwe-smt50-60k.json", 893, 2, "INT32", []byte{0x00, 0x00, 0x00, 0x00}, 0x03},
		// 固德威 MTG2 协议 293 为 U16，增益 10%；100% 对应 raw 1000。
		{"固德威MTG2功率百分比100", "goodwe-mtg2.json", 293, 1, "UINT16", []byte{0x03, 0xE8}, 0x03},
		// 固德威 SDTG2 协议 560 为 U16，增益 100 Hz；50 Hz 对应 raw 5000。
		{"固德威SDTG2频率50Hz", "goodwe-sdtg2.json", 560, 1, "UINT16", []byte{0x13, 0x88}, 0x03},
		// 奥太协议的交流频率最小单位为 0.01 Hz；49.55 Hz 对应 raw 4955。
		{"奥太频率49点55Hz", "aotai-hf-tl-telemetry.json", 16, 1, "UINT16", []byte{0x13, 0x5B}, 0x03},
		// 奥太小机功率点为 0.01 kW；TLD 的 5 kW 对应 raw 500。
		{"奥太TLD功率5kW", "aotai-tld-telemetry.json", 17, 1, "UINT16", []byte{0x01, 0xF4}, 0x03},
		// 奥太 TLC 的 15.55 kW 对应 raw 1555。
		{"奥太TLC功率15点55kW", "aotai-tlc10-30-telemetry.json", 17, 1, "UINT16", []byte{0x06, 0x13}, 0x03},
		// 奥太大机功率点最小单位为 20 W；200 kW 对应 raw 10000。
		{"奥太ASP功率200kW", "aotai-asp250-telemetry.json", 17, 1, "UINT16", []byte{0x27, 0x10}, 0x03},
		// 奥太累计发电量为 U32，独立样值 raw 120000 为 0001 D4C0。
		{"奥太累计发电量32位字序", "aotai-hf-tl-telemetry.json", 24, 2, "UINT32", []byte{0x00, 0x01, 0xD4, 0xC0}, 0x03},
		// 古瑞瓦特 TL3-X 三相表有功功率在输入区地址 35，raw 123456 为 0001 E240。
		{"古瑞瓦特MID有功功率", "growatt-tl3x-mid-mac.json", 35, 2, "UINT32", []byte{0x00, 0x01, 0xE2, 0x40}, 0x04},
		// 古瑞瓦特 MAX 同样用输入区地址 35，防止误用保持寄存器区或型号地址。
		{"古瑞瓦特MAX有功功率", "growatt-tl3x-max.json", 35, 2, "UINT32", []byte{0x00, 0x01, 0xE2, 0x40}, 0x04},
		// 古瑞瓦特 TL3-X 地址 232 明确为有符号 I32，-12345 编码为 FFFF CFC7。
		{"古瑞瓦特有符号无功", "growatt-tl3x-mid-mac.json", 232, 2, "INT32", []byte{0xFF, 0xFF, 0xCF, 0xC7}, 0x04},
		// 古瑞瓦特 TL-X 单相表功率地址为 3023，不能复用三相表地址 35。
		{"古瑞瓦特MIN单相有功功率", "growatt-tlx-min.json", 3023, 2, "UINT32", []byte{0x00, 0x01, 0xE2, 0x40}, 0x04},
		// 古瑞瓦特 TL-X 地址 3025 的 50 Hz 对应 raw 5000。
		{"古瑞瓦特MIN频率50Hz", "growatt-tlx-min.json", 3025, 1, "UINT16", []byte{0x13, 0x88}, 0x04},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			slave := loadInverterTestSlave(t, test.file)
			point := slave.Find(test.function, test.address)
			if point == nil {
				t.Fatalf("协议功能码 0x%02X 地址 %d 缺失", test.function, test.address)
			}
			if point.Type.String() != test.dataType {
				t.Fatalf("协议数据类型错误: 得到 %s，期望 %s", point.Type.String(), test.dataType)
			}
			assertInverterRegisterBytes(t, slave, test.function, test.address, test.quantity, test.want)
		})
	}
}

// 验证固德威 SMT 的 I32 控制点支持负值写入，并从保持寄存器区按补码读回。
func TestGoodweSMTControlPointSignedWriteReadBack(t *testing.T) {
	slave := loadInverterTestSlave(t, "goodwe-smt50-60k.json")
	point := slave.Find(0x03, 42404)
	if point == nil || point.Type != TypeInt32 || !point.Writable {
		t.Fatal("协议地址 42404 必须是可写的 INT32 控制点")
	}
	// 协议地址 42404 为 0xA5A4，-12345 的 I32 补码为 FFFF CFC7。
	frame := modbus.AppendCRC([]byte{1, 0x10, 0xA5, 0xA4, 0, 2, 4, 0xFF, 0xFF, 0xCF, 0xC7})
	result := slave.Handle(frame)
	wantAck := modbus.AppendCRC([]byte{1, 0x10, 0xA5, 0xA4, 0, 2})
	if !bytes.Equal(result.Response, wantAck) {
		t.Fatalf("负值控制写入响应错误: 得到 % X，期望 % X，摘要=%s", result.Response, wantAck, result.Summary)
	}
	assertInverterRegisterBytes(t, slave, 0x03, 42404, 2, []byte{0xFF, 0xFF, 0xCF, 0xC7})
}

// 加载厂商文件中的站号 1，从站只在内存中运行，不创建串口或改动现场配置。
func loadInverterTestSlave(t *testing.T, file string) *Slave {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", file))
	if err != nil {
		t.Fatalf("厂商配置加载失败: %v", err)
	}
	for _, bus := range cfg.Buses {
		for _, station := range bus.Slaves {
			if station.DeviceID == 1 {
				slave, err := NewSlave(station, time.Now())
				if err != nil {
					t.Fatalf("厂商从站构造失败: %v", err)
				}
				return slave
			}
		}
	}
	t.Fatalf("厂商文件 %s 缺少站号 1", file)
	return nil
}

// 对照独立的原始数据字节，验证站号、功能码、数据长度和完整响应 CRC。
func assertInverterRegisterBytes(t *testing.T, slave *Slave, function byte, address, quantity uint16, want []byte) {
	t.Helper()
	result := slave.Handle(req(1, function, address, quantity))
	wantFrame := modbus.AppendCRC(append([]byte{1, function, byte(len(want))}, want...))
	if !bytes.Equal(result.Response, wantFrame) {
		t.Fatalf("协议地址 %d 原始响应不符: 得到 % X，期望 % X，摘要=%s", address, result.Response, wantFrame, result.Summary)
	}
}

// 验证所有厂商示例能通过严格配置校验，且每个点在所属区域能完整读回。
func TestInverterConfigurationsLoadAndReadEveryPoint(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "configs", "inverters", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("未找到厂商逆变器配置示例")
	}
	combined := filepath.Join("..", "..", "config.inverters.example.json")
	if _, err := os.Stat(combined); err == nil {
		paths = append(paths, combined)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("厂商配置加载失败: %v", err)
			}
			for _, bus := range cfg.Buses {
				for _, station := range bus.Slaves {
					t.Run(fmt.Sprintf("%s/站号%d", bus.Name, station.DeviceID), func(t *testing.T) {
						if len(station.Registers) == 0 {
							t.Fatal("厂商从站没有任何点位")
						}
						slave, err := NewSlave(station, time.Now())
						if err != nil {
							t.Fatalf("厂商从站构造失败: %v", err)
						}
						for _, point := range station.Registers {
							t.Run(fmt.Sprintf("%s/地址%d/%s", point.Function, point.Address, point.Tag), func(t *testing.T) {
								fn, err := config.ParseFunction(point.Function)
								if err != nil {
									t.Fatal(err)
								}
								// 读写保持寄存器共用区域，写功能码点应由 0x03 读取。
								switch fn {
								case 0x05:
									fn = 0x01
								case 0x06, 0x10:
									fn = 0x03
								}
								quantity := uint16(1)
								if point.Type == "INT32" || point.Type == "UINT32" {
									quantity = 2
								}
								result := slave.Handle(req(station.DeviceID, fn, point.Address, quantity))
								if result.Response == nil || result.IsException {
									t.Fatalf("点位读取失败: 原因=%s 摘要=%s 响应=% X", result.DropReason, result.Summary, result.Response)
								}
								response := result.Response
								if !modbus.CheckCRC(response) {
									t.Fatalf("点位响应 CRC 错误: % X", response)
								}
								byteCount := int(quantity) * 2
								if fn == 0x01 {
									byteCount = (int(quantity) + 7) / 8
								}
								if len(response) != 3+byteCount+2 {
									t.Fatalf("点位响应长度错误: 得到 %d，期望 %d", len(response), 3+byteCount+2)
								}
								if response[0] != station.DeviceID || response[1] != fn || int(response[2]) != byteCount {
									t.Fatalf("点位响应站号、功能码或字节数错误: % X", response)
								}
							})
						}
					})
				}
			}
		})
	}
}
