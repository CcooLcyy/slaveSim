// 页面只做单位换算与展示；模型推进和控制效果由服务器决定。
export const scenarioFields = [
  { key: 'dc_available_kw', label: '太阳可用直流功率（kW）', min: 0, max: 100, step: 'any' },
  { key: 'grid_line_voltage_v', label: '电网线电压（V）', min: 100, max: 1000, step: 'any' },
  { key: 'grid_frequency_hz', label: '电网频率（Hz）', min: 40, max: 70, step: 'any' },
  { key: 'ambient_temperature_c', label: '环境温度（℃）', min: -40, max: 80, step: 'any' },
  { key: 'efficiency', label: '转换效率（0～1，不含0）', min: 0, max: 1, step: 'any', exclusiveMin: true },
];

export const controlFields = [
  { key: 'percent', label: '有功百分比（%）', address: 40125, gain: 10, min: 0, max: 100, step: '0.1', initial: 100 },
  { key: 'fixed', label: '固定有功（kW）', address: 40126, gain: 1000, min: 0, max: 55, step: '0.001', initial: 10 },
  { key: 'pf', label: '功率因数（-1＜PF≤-0.8 或 0.8≤PF≤1）', address: 40122, gain: 1000, min: -0.999, max: 1, step: '0.001', initial: 0.9 },
  { key: 'ratio', label: 'Q/S（-1～1）', address: 40123, gain: 1000, min: -1, max: 1, step: '0.001', initial: 0 },
  { key: 'active_gradient', label: '有功梯度（%/s）', address: 42017, gain: 1000, min: 0.1, max: 1000, step: '0.001', initial: 10 },
  { key: 'reactive_gradient', label: '无功梯度（%/s）', address: 42015, gain: 1000, min: 0.1, max: 1000, step: '0.001', initial: 10 },
];

function number(value, label) {
  if (value === null || value === undefined || typeof value === 'boolean' || String(value).trim() === '' || !Number.isFinite(Number(value))) throw new Error(`${label}不能为空且必须是有限数字`);
  return Number(value);
}

export function modelDevices(state) {
  return (state?.buses || []).flatMap(bus => (bus.slaves || [])
    .filter(slave => slave.model?.kind === 'huawei_v3')
    .map(slave => ({ ...slave, bus: bus.name, key: JSON.stringify([bus.name, slave.device_id]) })));
}

export function controlRequest(device, kind, input) {
  const field = controlFields.find(field => field.key === kind);
  if (!field) throw new Error('不支持的控制命令');
  const value = number(input, field.label);
  if (value < field.min || value > field.max || (kind === 'pf' && (value === -1 || Math.abs(value) < 0.8))) throw new Error(`${field.label}超出合法范围`);
  const scaled = value * field.gain;
  if (Math.abs(scaled - Math.round(scaled)) > 1e-7) throw new Error(`${field.label}精度过高，请按输入步长填写`);
  return { bus: device.bus, device_id: device.device_id, function: '0x03', address: field.address, value: Math.round(scaled) };
}

export function scenarioRequest(device, values) {
  const scenario = {};
  for (const field of scenarioFields) {
    const value = number(values[field.key], field.label);
    if (value < field.min || value > field.max || (field.exclusiveMin && value === field.min)) throw new Error(`${field.label}超出合法范围`);
    scenario[field.key] = value;
  }
  return { bus: device.bus, device_id: device.device_id, scenario };
}

const formatted = (value, digits, unit = '') => typeof value === 'number' && Number.isFinite(value) ? `${value.toFixed(digits)}${unit ? ` ${unit}` : ''}` : '—';
const commandNames = { 40125: '百分比限功率', 40120: '固定有功', 40126: '固定有功', 40122: '功率因数', 40123: 'Q/S' };
const command = address => address == null ? '—' : `${commandNames[address] || '控制命令'} · ${address}`;

export function getDeviceView(device) {
  const model = device.model;
  const read = (address, gain, digits, unit) => {
    const register = (device.registers || []).find(reg => reg.address === address);
    return formatted(register ? Number(register.value) / gain : undefined, digits, unit);
  };
  return {
    active: formatted(model.active_kw, 2, 'kW'), targetActive: formatted(model.target_active_kw, 2, 'kW'),
    reactive: formatted(model.reactive_kvar, 2, 'kvar'), targetReactive: formatted(model.target_reactive_kvar, 2, 'kvar'),
    powerFactor: formatted(model.power_factor, 3), dayEnergy: formatted(model.day_energy_kwh, 3, 'kWh'),
    totalEnergy: formatted(model.total_energy_kwh, 3, 'kWh'), status: model.status || '—',
    activeCommand: command(model.active_command), reactiveCommand: command(model.reactive_command),
    phases: ['A / AB', 'B / BC', 'C / CA'].map((name, index) => ({ name,
      lineVoltage: read(32066 + index, 10, 1, 'V'), phaseVoltage: read(32069 + index, 10, 1, 'V'),
      current: read(32072 + index * 2, 1000, 3, 'A'),
    })),
  };
}

// 趋势仅保存收到的服务器反馈，不插值或在浏览器中推进设备。
export function appendPowerSample(samples, state, device) {
  const time = Date.parse(state.now);
  const active = device.model.active_kw;
  const target = device.model.target_active_kw;
  if (![time, active, target].every(Number.isFinite) || (samples.length && time <= samples.at(-1).time)) return samples;
  return [...samples, { time, active, target }].slice(-60);
}

export function powerTrendView(samples) {
  const maximum = Math.ceil(Math.max(55, ...samples.flatMap(sample => [sample.active, sample.target])) / 10) * 10;
  if (samples.length < 2) return { active: '', target: '', maximum, caption: '等待后续反馈采样…' };
  const first = samples[0].time;
  const duration = samples.at(-1).time - first;
  const points = key => samples.map(sample => `${((sample.time - first) / duration * 320).toFixed(1)},${(120 - Math.max(0, sample[key]) / maximum * 120).toFixed(1)}`).join(' ');
  return { active: points('active'), target: points('target'), maximum,
    caption: `${samples.length} 次反馈 · ${(duration / 1000).toFixed(0)} 秒 · 纵轴 0～${maximum} kW`,
  };
}

export async function sendHuaweiRequest(fetch, endpoint, payload) {
  let response;
  try {
    response = await fetch(endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
  } catch { throw new Error('连接失败，请检查模拟器连接；操作结果未知，请查看反馈后再重试'); }
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `请求失败（${response.status}）`);
  return body;
}
