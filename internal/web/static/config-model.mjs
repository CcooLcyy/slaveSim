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

export function newBus(buses = []) {
  let number = 1;
  while (buses.some(bus => bus.name === `RS485-${number}`)) number++;
  const firstLink = buses[0]?.link || '/srv/serial-sim/tty0';
  const directory = firstLink.slice(0, firstLink.lastIndexOf('/') + 1);
  let port = 0;
  while (buses.some(bus => bus.link === `${directory}tty${port}`)) port++;
  return { name: `RS485-${number}`, link: `${directory}tty${port}`, slaves: [newSlave()] };
}

export function normalizeConfig(value) {
  const copy = structuredClone(value);
  return { buses: (copy.buses || []).map(bus => ({
    ...bus, slaves: (bus.slaves || []).map(slave => ({
      response_delay_ms: 0, ...slave, unmapped_policy: slave.unmapped_policy || 'exception',
      registers: (slave.registers || []).map(reg => {
        const defaults = newRegister();
        const result = { ...defaults, ...reg, source: { ...defaults.source, ...reg.source } };
        result.function = `0x${Number.parseInt(result.function, 16).toString(16).padStart(2, '0').toUpperCase()}`;
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
// 最终校验仍由服务器完成，页面先提示常见输入错误并保留草稿。
export function validateDraft(config) {
  if (!config.buses?.length) return '请至少配置一路 RS485 总线';
  const names = new Set();
  const paths = new Set();
  for (const bus of config.buses) {
    if (!bus.name.trim() || !bus.link.trim()) return '总线名称和串口路径不能为空';
    if (names.has(bus.name.trim()) || paths.has(bus.link.trim())) return `总线 ${bus.name} 的名称或串口路径重复`;
    names.add(bus.name.trim()); paths.add(bus.link.trim());
    if (!bus.slaves?.length) return `总线 ${bus.name} 请至少添加一个从站`;
    const ids = new Set();
    for (const slave of bus.slaves) {
      const prefix = `总线 ${bus.name} / 站号 ${slave.device_id}`;
      if (!integerIn(slave.device_id, 1, 247)) return `${prefix}：站号必须为 1～247 的整数`;
      if (ids.has(slave.device_id)) return `${prefix}：站号重复`;
      ids.add(slave.device_id);
      if (!integerIn(slave.response_delay_ms, 0, 60000)) return `${prefix}：响应延迟必须为 0～60000 毫秒的整数`;
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
