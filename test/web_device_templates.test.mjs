import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { nextDeviceID, parseDeviceTemplates, createDeviceSlave, loadDeviceTemplates } from '../internal/web/static/device-templates.mjs';

const huawei = JSON.parse(readFileSync(new URL('../configs/inverters/huawei-v3-model.json', import.meta.url), 'utf8')).buses[0].slaves[0];
const template = { id: 'huawei_v3', name: '华为逆变器（V3，行为仿真）', slave: huawei };

test('站号只在本总线内分配，选择1到247的最小空闲值，用满明确报错', () => {
  assert.equal(nextDeviceID([{ device_id: 1 }, { device_id: 3 }]), 2);
  assert.equal(nextDeviceID([]), 1);
  assert.equal(nextDeviceID(Array.from({ length: 246 }, (_, index) => ({ device_id: index + 1 }))), 247);
  assert.throws(() => nextDeviceID(Array.from({ length: 247 }, (_, index) => ({ device_id: index + 1 }))), /1～247.*已用满/);
});

test('自定义Modbus和预置设备均新增草稿，保留原从站对象和全部配置', () => {
  const existing = { device_id: 1, registers: [{ address: 3004, source: { kind: 'manual', value: 7 } }] };
  const slaves = [existing];
  const before = structuredClone(slaves);
  const custom = createDeviceSlave(slaves);
  assert.equal(custom.device_id, 2);
  assert.equal(custom.model, undefined);
  assert.equal(custom.registers.length, 1);
  const preset = createDeviceSlave(slaves, template);
  assert.equal(preset.device_id, 2);
  assert.equal(preset.model.kind, 'huawei_v3');
  assert.equal(preset.registers.length, huawei.registers.length);
  assert.deepEqual(slaves, before);
  assert.equal(slaves[0], existing);
});

test('多台预置设备的模型、寄存器、数值来源互不共享，也不污染目录模板', () => {
  const first = createDeviceSlave([], template);
  const second = createDeviceSlave([first], template);
  first.model.dc_available_kw = 12;
  first.registers[0].source.value = 777;
  assert.equal(second.device_id, 2);
  assert.equal(second.model.dc_available_kw, huawei.model.dc_available_kw);
  assert.equal(second.registers[0].source.value, huawei.registers[0].source.value);
  assert.notEqual(first.model, second.model);
  assert.notEqual(first.registers[0].source, second.registers[0].source);
  assert.notEqual(first.registers[0], huawei.registers[0]);
});

test('目录兼容行为与普通点表预置，名称及完整点表由服务端提供', () => {
  const staticSlave = structuredClone(huawei); delete staticSlave.model;
  const original = [template, { id: 'huawei_v3_static', name: '华为逆变器（V3，静态点表）', slave: staticSlave }];
  const catalog = parseDeviceTemplates(original);
  assert.deepEqual(catalog.map(item => [item.id, item.name]), original.map(item => [item.id, item.name]));
  assert.equal(createDeviceSlave([], catalog[1]).model, undefined);
  catalog[0].slave.model.dc_available_kw = 1;
  assert.equal(original[0].slave.model.dc_available_kw, huawei.model.dc_available_kw);
});

test('仓库全部厂商点表均可解析并独立加入设备，普通点表不会产生模型', () => {
  const directory = new URL('../configs/inverters/', import.meta.url);
  const templates = readdirSync(directory).filter(name => name.endsWith('.json')).map(file => ({
    id: file, name: file,
    slave: JSON.parse(readFileSync(new URL(file, directory), 'utf8')).buses[0].slaves[0],
  }));
  const parsed = parseDeviceTemplates(templates);
  assert.ok(parsed.length >= 18);
  for (const item of parsed) {
    const added = createDeviceSlave([{ device_id: 1 }], item);
    assert.equal(added.device_id, 2);
    assert.equal(added.registers.length, item.slave.registers.length);
    assert.equal(added.model?.kind, item.slave.model?.kind);
  }
});

test('目录格式错误、重复标识和空寄存器模板不会加入设备', () => {
  for (const value of [null, {}, [{ ...template, id: '' }], [{ ...template, name: '' }], [template, template], [{ ...template, slave: { registers: [] } }]]) assert.throws(() => parseDeviceTemplates(value));
  assert.throws(() => createDeviceSlave([], { ...template, slave: {} }));
});

test('读取目录只GET，不保存配置；HTTP、JSON与网络错误提供可用提示', async () => {
  const calls = [];
  const catalog = await loadDeviceTemplates(async (url, options) => { calls.push({ url, options }); return { ok: true, json: async () => [template] }; });
  assert.equal(catalog[0].id, 'huawei_v3');
  assert.deepEqual(calls, [{ url: '/api/device-templates', options: { method: 'GET', cache: 'no-store' } }]);
  await assert.rejects(loadDeviceTemplates(async () => ({ ok: false, status: 404, json: async () => ({ error: '设备预置不可用' }) })), /设备预置不可用/);
  await assert.rejects(loadDeviceTemplates(async () => ({ ok: true, json: async () => { throw new SyntaxError(); } })), /格式/);
  await assert.rejects(loadDeviceTemplates(async () => { throw new TypeError('Failed to fetch'); }), /连接失败/);
});
