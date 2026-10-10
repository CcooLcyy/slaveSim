import test from 'node:test';
import assert from 'node:assert/strict';
import { newBus, newSlave, newRegister, normalizeConfig, validateDraft, sourceFields } from '../internal/web/static/config-model.mjs';

// 多路总线生成独立名称和路径，且从站站号可以跨总线重复。
test('新增总线沿用挂载目录并避开既有名称和路径', () => {
  const buses = [{ name: 'RS485-1', link: '/home/megsky/serial-sim/tty0', slaves: [newSlave()] }];
  const second = newBus(buses);
  assert.equal(second.name, 'RS485-2');
  assert.equal(second.link, '/home/megsky/serial-sim/tty1');
  assert.equal(second.slaves[0].device_id, 1);
  assert.equal(validateDraft({ buses: [...buses, second] }), '');
});

// 新从站和32位寄存器的地址分配不能与已有配置重叠。
test('新增从站和寄存器避开已占用编号', () => {
  assert.equal(newSlave([{ device_id: 1 }, { device_id: 3 }]).device_id, 2);
  assert.equal(newRegister([{ address: 40, type: 'UINT32' }]).address, 42);
});

// 读取配置时补齐缺省值并深复制，草稿修改不能污染原配置。
test('配置草稿补齐默认字段并保持隔离', () => {
  const original = { buses: [{ name: '一路', link: '/tmp/tty0', slaves: [{ device_id: 2, registers: [{ function: '0x03', address: 0 }] }] }] };
  const draft = normalizeConfig(original);
  const reg = draft.buses[0].slaves[0].registers[0];
  assert.equal(reg.source.kind, 'const');
  assert.equal(reg.type, 'UINT16');
  reg.source.value = 42;
  assert.equal(original.buses[0].slaves[0].registers[0].source, undefined);
});

// Go 返回省略字段的零值时，选择框仍显示有效默认选项。
test('服务器空字符串策略恢复为默认异常响应', () => {
  const cfg = { buses: [{ name: '一路', link: '/tmp/tty0', slaves: [{ device_id: 1, unmapped_policy: '', registers: [] }] }] };
  assert.equal(normalizeConfig(cfg).buses[0].slaves[0].unmapped_policy, 'exception');
});

// UI 在保存前阻止空列表、重复路径、非法编号和来源参数。
test('非法配置返回可定位的中文原因', () => {
  assert.match(validateDraft({ buses: [] }), /总线/);
  const cfg = { buses: [newBus([])] };
  cfg.buses.push(structuredClone(cfg.buses[0]));
  assert.match(validateDraft(cfg), /重复/);
  cfg.buses.pop();
  cfg.buses[0].slaves[0].device_id = 248;
  assert.match(validateDraft(cfg), /站号/);
  cfg.buses[0].slaves[0].device_id = 1;
  cfg.buses[0].slaves[0].registers[0].source.kind = 'ramp';
  cfg.buses[0].slaves[0].registers[0].source.min = 10;
  cfg.buses[0].slaves[0].registers[0].source.max = 1;
  assert.match(validateDraft(cfg), /最大值/);
});

// 各类来源只显示实际需要的参数，避免配置者误用。
test('全部数值来源具有对应参数', () => {
  assert.deepEqual(sourceFields('const'), ['value']);
  assert.deepEqual(sourceFields('manual'), ['value']);
  assert.deepEqual(sourceFields('sine'), ['base', 'amplitude', 'period_ms']);
  assert.deepEqual(sourceFields('ramp'), ['min', 'max', 'period_ms']);
  assert.deepEqual(sourceFields('random'), ['min', 'max', 'period_ms']);
});
