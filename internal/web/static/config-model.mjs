// 配置草稿与默认值独立于 DOM，便于验证多路配置规则。
export function newRegister(registers = []) {
  const address = registers.reduce((next, reg) => Math.max(next, reg.address + (['UINT32', 'INT32'].includes(reg.type) ? 2 : 1)), 0);
  return {
    function: '0x03', address, type: 'UINT16', word_order: 'HL', byte_order: 'AB',
    bit_index: 0, access: 'readonly', tag: '', unit: '',
    source: { kind: 'const', value: 0, base: 0, amplitude: 1, period_ms: 1000, min: 0, max: 100 },
  };
}

export function newSlave(slaves = []) {
  let device_id = 1;
  while (slaves.some(slave => slave.device_id === device_id)) device_id++;
  return { device_id, response_delay_ms: 0, unmapped_policy: 'exception', registers: [newRegister()] };
}

export const qVoltagePoints = [
  { key: 'command', label: '无功设定', functions: ['0x03'], address: 2 },
  { key: 'measured_q', label: '无功实测', functions: ['0x03', '0x04'], address: 1 },
  { key: 'voltage', label: '并网点电压', functions: ['0x03', '0x04'], address: 0 },
];

export const qVoltageParameters = [
  { key: 'base_voltage_v', label: '基准电压（V）', initial: 220 },
  { key: 'volts_per_kvar', label: '电压灵敏度（V/kvar）', initial: 0.25 },
  { key: 'min_kvar', label: '无功下限（kvar）', initial: -100 },
  { key: 'max_kvar', label: '无功上限（kvar）', initial: 100 },
  { key: 'ramp_kvar_per_second', label: '无功变化速率（kvar/s）', initial: 100 },
];

export function newQVoltage() {
  return {
    ...Object.fromEntries(qVoltagePoints.map(({ key, functions, address }) => [key, { function: functions[0], address, scale: 0.1 }])),
    ...Object.fromEntries(qVoltageParameters.map(({ key, initial }) => [key, initial])),
  };
}

export function newBus(buses = []) {
  let number = 1;
  while (buses.some(bus => bus.name === `RS485-${number}`)) number++;
  const firstLink = buses[0]?.link || '/srv/serial-sim/tty0';
  const directory = firstLink.slice(0, firstLink.lastIndexOf('/') + 1);
  let port = 0;
  while (buses.some(bus => bus.link === `${directory}tty${port}`)) port++;
  return { name: `RS485-${number}`, link: `${directory}tty${port}`, slaves: [] };
}

const normalizeFunction = value => `0x${Number.parseInt(value, 16).toString(16).padStart(2, '0').toUpperCase()}`;

function normalizeQVoltage(scene) {
  for (const { key } of qVoltagePoints) {
    if (scene[key]) scene[key].function = normalizeFunction(scene[key].function);
  }
  return scene;
}

export function normalizeConfig(value) {
  const copy = structuredClone(value);
  return { buses: (copy.buses || []).map(bus => ({
    ...bus, slaves: (bus.slaves || []).map(slave => ({
      response_delay_ms: 0, ...slave, unmapped_policy: slave.unmapped_policy || 'exception',
      ...(slave.q_voltage ? { q_voltage: normalizeQVoltage(slave.q_voltage) } : {}),
      registers: (slave.registers || []).map(reg => {
        const defaults = newRegister();
        const result = { ...defaults, ...reg, source: { ...defaults.source, ...reg.source } };
        result.function = normalizeFunction(result.function);
        result.type = (result.type || 'UINT16').toUpperCase();
        result.access = (result.access || 'readonly').toLowerCase();
        result.word_order = (result.word_order || 'HL').toUpperCase();
        result.byte_order = (result.byte_order || 'AB').toUpperCase();
        result.source.kind ||= 'const';
        return result;
      }),
    })),
  })) };
}

export function sourceFields(kind) {
  if (kind === 'sine') return ['base', 'amplitude', 'period_ms'];
  if (kind === 'ramp' || kind === 'random') return ['min', 'max', 'period_ms'];
  return ['value'];
}

const integerIn = (value, min, max) => Number.isInteger(value) && value >= min && value <= max;

function validateQVoltage(slave) {
  if (slave.model) return '无功–电压联动不能与设备行为模型同时启用';
  const scene = slave.q_voltage;
  const addresses = new Set();
  for (const { key, label, functions } of qVoltagePoints) {
    const point = scene[key];
    if (!point || !functions.includes(point.function)) return `${label}功能码无效`;
    if (!integerIn(point.address, 0, 65535)) return `${label}协议地址必须为 0～65535 的整数`;
    if (!Number.isFinite(point.scale) || point.scale <= 0) return `${label}倍率必须为正数`;
    const address = `${point.function}:${point.address}`;
    if (addresses.has(address)) return '无功–电压联动的三个寄存器不能重复';
    addresses.add(address);
  }
  for (const { key, label } of qVoltageParameters) {
    if (!Number.isFinite(scene[key])) return `${label}不能为空且必须为有限数字`;
  }
  return '';
}

// 最终校验仍由服务器完成，页面先提示常见输入错误并保留草稿。
export function validateDraft(config) {
  if (!Array.isArray(config.buses)) return '总线配置必须是列表';
  const names = new Set();
  const paths = new Set();
  for (const bus of config.buses) {
    if (!bus.name.trim() || !bus.link.trim()) return '总线名称和串口路径不能为空';
    if (names.has(bus.name.trim()) || paths.has(bus.link.trim())) return `总线 ${bus.name} 的名称或串口路径重复`;
    names.add(bus.name.trim()); paths.add(bus.link.trim());
    if (!Array.isArray(bus.slaves)) return `总线 ${bus.name} 的从站配置必须是列表`;
    const ids = new Set();
    for (const slave of bus.slaves) {
      const prefix = `总线 ${bus.name} / 站号 ${slave.device_id}`;
      if (!integerIn(slave.device_id, 1, 247)) return `${prefix}：站号必须为 1～247 的整数`;
      if (ids.has(slave.device_id)) return `${prefix}：站号重复`;
      ids.add(slave.device_id);
      if (!integerIn(slave.response_delay_ms, 0, 60000)) return `${prefix}：响应延迟必须为 0～60000 毫秒的整数`;
      if (slave.q_voltage) {
        const error = validateQVoltage(slave);
        if (error) return `${prefix}：${error}`;
      }
      for (const reg of slave.registers) {
        if (!integerIn(reg.address, 0, ['UINT32', 'INT32'].includes(reg.type) ? 65534 : 65535)) return `${prefix}：寄存器地址超范围或不是整数`;
        if (!integerIn(reg.bit_index, 0, 15)) return `${prefix}：BOOL 位号必须为 0～15 的整数`;
        for (const field of sourceFields(reg.source.kind)) {
          if (!Number.isFinite(reg.source[field])) return `${prefix} / 地址 ${reg.address}：数值来源参数不能为空且必须为有限数字`;
        }
        if (reg.source.kind === 'sine' && reg.source.amplitude < 0) return `${prefix}：振幅不能为负数`;
        if (['sine', 'ramp', 'random'].includes(reg.source.kind) && !integerIn(reg.source.period_ms, 1, 2147483647)) return `${prefix}：周期必须为正整数毫秒`;
        if (['ramp', 'random'].includes(reg.source.kind) && reg.source.min > reg.source.max) return `${prefix}：最大值不能小于最小值`;
      }
    }
  }
  return '';
}
