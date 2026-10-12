import test from 'node:test';
import assert from 'node:assert/strict';
import { modelDevices, controlRequest, scenarioRequest, getDeviceView, sendHuaweiRequest, appendPowerSample, powerTrendView } from '../internal/web/static/huawei-view.mjs';
import { normalizeConfig } from '../internal/web/static/config-model.mjs';

const scenario = { dc_available_kw: 51.02, grid_line_voltage_v: 400, grid_frequency_hz: 50, ambient_temperature_c: 25, efficiency: 0.98 };
const device = { bus: 'Huawei-V3', device_id: 1, model: { kind: 'huawei_v3', active_kw: 25, target_active_kw: 25, reactive_kvar: -12.1, target_reactive_kvar: -12.1, power_factor: -0.9, day_energy_kwh: 1.2, total_energy_kwh: 54321.2, status: '限电降额', active_command: 40125, reactive_command: 40122, scenario }, registers: [{ address: 30075, value: 55000 }] };

test('已有配置归一化保留模型五参数，草稿编辑不会改动原配置', () => {
  const model = { kind: 'huawei_v3', ...scenario };
  const source = { buses: [{ name: 'Huawei-V3', slaves: [{ device_id: 1, model, registers: [] }] }] };
  const draft = normalizeConfig(source);
  assert.deepEqual(draft.buses[0].slaves[0].model, model);
  draft.buses[0].slaves[0].model.dc_available_kw = 20;
  assert.equal(model.dc_available_kw, 51.02);
});

test('只为华为行为设备建立稳定标识，旧固定点表不显示控制卡', () => {
  const state = { buses: [{ name: 'Huawei-V3', slaves: [{ device_id: 1, model: device.model }, { device_id: 2 }] }] };
  assert.deepEqual(modelDevices(state).map(item => [item.bus, item.device_id, item.key]), [['Huawei-V3', 1, '["Huawei-V3",1]']]);
  assert.deepEqual(modelDevices({}), []);
});

test('预设和工程量换算为实际寄存器原始值，负PF和Q/S保持符号', () => {
  for (const [kind, value, address, raw] of [['percent', 50, 40125, 500], ['fixed', 10, 40126, 10000], ['percent', 100, 40125, 1000], ['pf', 0.9, 40122, 900], ['pf', -0.8, 40122, -800], ['ratio', -0.5, 40123, -500], ['active_gradient', 10, 42017, 10000], ['reactive_gradient', 0.1, 42015, 100]]) {
    assert.deepEqual(controlRequest(device, kind, value), { bus: device.bus, device_id: 1, function: '0x03', address, value: raw });
  }
  assert.equal(controlRequest(device, 'pf', -0.999).value, -999);
});

test('非法控制、空值和不能精确编码的数值在发送前拒绝', () => {
  for (const [kind, value] of [['percent', ''], ['percent', ' '], ['percent', 100.1], ['fixed', -1], ['fixed', 55.001], ['pf', -1], ['pf', 0.79], ['pf', -0.79], ['ratio', 1.1], ['ratio', 0.1234], ['active_gradient', 0], ['reactive_gradient', 1001], ['unknown', 1]]) {
    assert.throws(() => controlRequest(device, kind, value));
  }
});

test('固定有功按当前Pmax反馈校验大小容量边界，缺少或非法能力拒绝发送', () => {
  for (const maximum of [11, 80, 100]) {
    const sized = { ...device, registers: [{ address: 30075, value: maximum * 1000 }] };
    assert.equal(controlRequest(sized, 'fixed', maximum).value, maximum * 1000);
    assert.throws(() => controlRequest(sized, 'fixed', maximum + 0.001), /超出合法范围/);
  }
  for (const registers of [[], [{ address: 30075 }], [{ address: 30075, value: null }], [{ address: 30075, value: NaN }], [{ address: 30075, value: -1000 }]]) {
    assert.throws(() => controlRequest({ ...device, registers }, 'fixed', 1), /最大有功功率/);
  }
});

test('同地址的输入寄存器反馈不能覆盖保持区域的Pmax', () => {
  const sized = { ...device, registers: [
    { function: '0x04', address: 30075, value: 1000 },
    { function: '0x03', address: 30075, value: 80000 },
  ] };
  assert.equal(controlRequest(sized, 'fixed', 80).value, 80000);
  assert.throws(() => controlRequest({ ...device, registers: [sized.registers[0]] }, 'fixed', 1));
});

test('有功趋势只记录服务端时间与实际/目标反馈，拒绝重复和乱序采样', () => {
  let samples = [];
  const model = { active_kw: 50, target_active_kw: 25 };
  samples = appendPowerSample(samples, { now: '2026-10-11T10:00:00Z' }, { model });
  samples = appendPowerSample(samples, { now: '2026-10-11T10:00:02Z' }, { model: { active_kw: 40, target_active_kw: 25 } });
  assert.deepEqual(samples.map(sample => [sample.active, sample.target]), [[50, 25], [40, 25]]);
  assert.equal(appendPowerSample(samples, { now: '2026-10-11T10:00:02Z' }, { model }), samples);
  assert.equal(appendPowerSample(samples, { now: '2026-10-11T09:59:59Z' }, { model }), samples);
  assert.equal(appendPowerSample(samples, {}, { model }), samples);
  assert.equal(appendPowerSample(samples, { now: '2026-10-11T10:00:03Z' }, { model: { active_kw: NaN, target_active_kw: 25 } }), samples);
  const view = powerTrendView(samples);
  assert.equal(view.active, '0.0,20.0 320.0,40.0');
  assert.equal(view.target, '0.0,70.0 320.0,70.0');
  assert.equal(view.maximum, 60);
  for (let index = 3; index < 80; index++) samples = appendPowerSample(samples, { now: new Date(Date.UTC(2026, 9, 11, 10, 0, index)).toISOString() }, { model });
  assert.equal(samples.length, 60);
});

test('场景提交包含全部五字段，验证范围并拒绝空输入', () => {
  assert.deepEqual(scenarioRequest(device, scenario), { bus: device.bus, device_id: 1, scenario });
  const ranges = { dc_available_kw: [-0.1, 100.1], grid_line_voltage_v: [99, 1001], grid_frequency_hz: [39, 71], ambient_temperature_c: [-41, 81], efficiency: [0, 1.01] };
  for (const [key, values] of Object.entries(ranges)) for (const value of [...values, '', ' ', NaN]) {
    assert.throws(() => scenarioRequest(device, { ...scenario, [key]: value }));
  }
  assert.throws(() => scenarioRequest(device, { dc_available_kw: 20 }));
});

test('反馈使用服务端P/Q与电量，三相仅解码寄存器，不从目标值推算', () => {
  const view = getDeviceView({ ...device, registers: [
    { address: 32066, value: 4000 }, { address: 32067, value: 4001 }, { address: 32068, value: 3999 },
    { address: 32069, value: 2309 }, { address: 32070, value: 2310 }, { address: 32071, value: 2308 },
    { address: 32072, value: 42000 }, { address: 32074, value: 42001 }, { address: 32076, value: 41999 },
  ] });
  assert.equal(view.active, '25.00 kW');
  assert.equal(view.reactive, '-12.10 kvar');
  assert.equal(view.powerFactor, '-0.900');
  assert.equal(view.activeCommand, '百分比限功率 · 40125');
  assert.equal(view.phases[0].lineVoltage, '400.0 V');
  assert.equal(view.phases[1].current, '42.001 A');
  assert.equal(getDeviceView(device).phases[0].current, '—');
});

test('客户端请求既有端点并传播中文响应错误与网络错误', async () => {
  const calls = [];
  const fetch = async (url, options) => { calls.push({ url, options }); return { ok: true, json: async () => ({ ok: true }) }; };
  await sendHuaweiRequest(fetch, '/api/manual', controlRequest(device, 'percent', 50));
  await sendHuaweiRequest(fetch, '/api/scenario', scenarioRequest(device, scenario));
  assert.deepEqual(calls.map(call => call.url), ['/api/manual', '/api/scenario']);
  assert.ok(calls.every(call => call.options.method === 'POST'));
  assert.equal(JSON.parse(calls[0].options.body).value, 500);
  await assert.rejects(sendHuaweiRequest(async () => ({ ok: false, status: 400, json: async () => ({ error: '功率因数超范围' }) }), '/api/manual', {}), /功率因数超范围/);
  await assert.rejects(sendHuaweiRequest(async () => ({ ok: false, status: 500, json: async () => { throw new Error(); } }), '/api/manual', {}), /500/);
  await assert.rejects(sendHuaweiRequest(async () => { throw new TypeError('Failed to fetch'); }, '/api/manual', {}), /连接失败/);
});
