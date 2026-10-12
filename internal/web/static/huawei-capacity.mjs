// 30073 是额定容量的唯一来源；这里只修改当前设备的配置草稿。
const addresses = [30073, 30075, 30077, 30079, 30081, 32078, 40120, 40126];
const holdingRegisters = (slave, address) => (slave?.registers || [])
  .filter(reg => reg.address === address && Number.parseInt(reg.function, 16) === 3);

export function ratedCapacity(slave) {
  const register = holdingRegisters(slave, 30073)[0];
  return register?.source ? register.source.value / 1000 : undefined;
}

function capacityInput(input) {
  if (!['number', 'string'].includes(typeof input) || String(input).trim() === '' || !Number.isFinite(Number(input))) {
    throw new Error('逆变器额定容量不能为空且必须是有限数字');
  }
  const capacity = Number(input);
  if (capacity < 1 || capacity > 100) throw new Error('逆变器额定容量超出范围，请输入 1～100 kW');
  const watts = capacity * 1000;
  if (Math.abs(watts - Math.round(watts)) > 1e-7) throw new Error('逆变器额定容量精度过高，请按 0.001 kW 步长填写');
  return Math.round(watts);
}

function currentValues(slave) {
  const registers = new Map();
  const values = new Map();
  for (const address of addresses) {
    const matches = holdingRegisters(slave, address);
    if (matches.length !== 1) throw new Error(`寄存器 ${address} 缺失或重复，无法修改额定容量`);
    const register = matches[0];
    const type = address === 40120 ? 'UINT16' : [30079, 30081, 32078].includes(address) ? 'INT32' : 'UINT32';
    const access = address >= 40120 ? 'readwrite' : 'readonly';
    if (register.type !== type || (register.access || 'readonly') !== access
      || (register.byte_order && register.byte_order !== 'AB') || (register.word_order && register.word_order !== 'HL')) {
      throw new Error(`寄存器 ${address} 的类型、权限或字节序不符合现有能力配置`);
    }
    if (!register.source || ![undefined, '', 'const', 'manual'].includes(register.source.kind)) {
      throw new Error(`寄存器 ${address} 的模型初值必须使用固定值或手动来源`);
    }
    const value = register.source.value;
    if (!Number.isFinite(value) || !Number.isInteger(value)) throw new Error(`寄存器 ${address} 初值必须是有限整数`);
    registers.set(address, register);
    values.set(address, value);
  }
  const read = address => values.get(address);
  const pn = read(30073), pmax = read(30075), smax = read(30077);
  if (pn < 1000 || pmax < pn || smax < pmax || smax > 100000) {
    throw new Error('现有功率能力必须满足 1 ≤ 额定容量 ≤ Pmax ≤ Smax ≤ 100（kW/kVA）');
  }
  if (read(30079) <= 0 || read(30079) > smax || read(30081) >= 0 || -read(30081) > smax) {
    throw new Error('现有无功能力必须正馈入、负吸收，且幅值不能超过 Smax');
  }
  if (read(32078) < 0 || read(32078) > pmax) throw new Error('当天峰值有功初值必须在 0～Pmax 范围内');
  if (read(40120) < 0 || read(40120) * 100 > pmax || read(40126) < 0 || read(40126) > pmax) {
    throw new Error('固定有功初值必须在 0～Pmax 范围内');
  }
  return { registers, read };
}

// 失败时抛出中文错误，不改变任何能力点或模型输入；成功返回新的 kW。
export function setRatedCapacity(slave, input) {
  const pn = capacityInput(input);
  const { registers, read } = currentValues(slave);
  const ratio = pn / read(30073);
  const scaled = address => Math.round(read(address) * ratio);
  const pmax = Math.max(pn, Math.min(100000, scaled(30075)));
  const smax = Math.max(pmax, Math.min(100000, scaled(30077)));
  const updates = new Map([
    [30073, pn], [30075, pmax], [30077, smax],
    [30079, Math.max(1, Math.min(smax, scaled(30079)))],
    [30081, Math.min(-1, Math.max(-smax, scaled(30081)))],
    [32078, Math.max(0, Math.min(pmax, scaled(32078)))],
    // 40120 每原始单位是100W；向能力范围内截断，不能四舍五入越过Pmax。
    [40120, Math.max(0, Math.min(Math.floor(pmax / 100), scaled(40120)))],
    [40126, Math.max(0, Math.min(pmax, scaled(40126)))],
  ]);
  const oldCapacity = read(30073) / 1000;
  const followDC = typeof slave.model?.dc_available_kw === 'number'
    && Math.abs(slave.model.dc_available_kw - oldCapacity) <= 1e-7;
  for (const [address, value] of updates) registers.get(address).source.value = value;
  if (followDC) slave.model.dc_available_kw = pn / 1000;
  return pn / 1000;
}
