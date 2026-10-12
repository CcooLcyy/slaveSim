import test from 'node:test';
import assert from 'node:assert/strict';
import { ratedCapacity, setRatedCapacity } from '../internal/web/static/huawei-capacity.mjs';

const addresses = [30073, 30075, 30077, 30079, 30081, 32078, 40120, 40126];
function device() {
  const values = [50000, 55000, 60000, 33000, -32000, 45000, 500, 42001];
  return { device_id: 1, model: { kind: 'huawei_v3', dc_available_kw: 50, efficiency: 0.98 },
    registers: addresses.map((address, index) => ({ function: '0x03', address,
      type: address === 40120 ? 'UINT16' : [30079, 30081, 32078].includes(address) ? 'INT32' : 'UINT32',
      access: address >= 40120 ? 'readwrite' : 'readonly', tag: `点 ${address}`,
      source: { kind: index % 2 ? 'manual' : 'const', value: values[index], custom: '保留' } })),
  };
}
const raw = (slave, address) => slave.registers.find(reg => reg.address === address).source.value;
const values = slave => addresses.map(address => raw(slave, address));

test('额定容量只读30073，已有容量与光照保持原值', () => {
  const slave = device(); slave.model.dc_available_kw = 51.02;
  const before = structuredClone(slave);
  assert.equal(ratedCapacity(slave), 50);
  assert.deepEqual(slave, before);
  assert.equal(ratedCapacity({ registers: [] }), undefined);
});

test('修改按Pn比例联动能力和功率初值，按寄存器精度取整并保留元数据', () => {
  const slave = device();
  slave.registers.push({ function: '0x03', address: 40125, source: { kind: 'manual', value: 875 } });
  const before = structuredClone(slave);
  const references = slave.registers.map(reg => reg.source);
  assert.equal(setRatedCapacity(slave, '12.345'), 12.345);
  assert.deepEqual(values(slave), [12345, 13580, 14814, 8148, -7901, 11111, 123, 10370]);
  assert.equal(slave.model.dc_available_kw, 12.345);
  slave.registers.forEach((reg, index) => {
    assert.equal(reg.source, references[index]);
    const expected = structuredClone(before.registers[index]);
    if (addresses.includes(reg.address)) expected.source.value = reg.source.value;
    assert.deepEqual(reg, expected);
  });
  assert.equal(slave.model.efficiency, 0.98);
});

test('100kW边界截断Pmax/Smax，Q与功率初值落在截断后的能力内', () => {
  const slave = device();
  slave.registers.find(reg => reg.address === 30079).source.value = 60000;
  slave.registers.find(reg => reg.address === 30081).source.value = -60000;
  slave.registers.find(reg => reg.address === 32078).source.value = 55000;
  slave.registers.find(reg => reg.address === 40120).source.value = 550;
  slave.registers.find(reg => reg.address === 40126).source.value = 55000;
  setRatedCapacity(slave, 100);
  assert.deepEqual(values(slave), [100000, 100000, 100000, 100000, -100000, 100000, 1000, 100000]);
  assert.equal(slave.model.dc_available_kw, 100);
});

test('1kW边界保留非零正负Q，粗步长固定有功不超过Pmax', () => {
  const slave = device();
  slave.registers.find(reg => reg.address === 30075).source.value = 50001;
  slave.registers.find(reg => reg.address === 30079).source.value = 1;
  slave.registers.find(reg => reg.address === 30081).source.value = -1;
  slave.registers.find(reg => reg.address === 40120).source.value = 500;
  setRatedCapacity(slave, 1.001);
  assert.deepEqual(values(slave).slice(0, 5), [1001, 1001, 1201, 1, -1]);
  assert.equal(raw(slave, 40120), 10);
  assert.ok(raw(slave, 40120) * 100 <= raw(slave, 30075));
  setRatedCapacity(slave, 1);
  assert.equal(ratedCapacity(slave), 1);
  assert.equal(raw(slave, 30079), 1);
  assert.equal(raw(slave, 30081), -1);
});

test('重复上下调整以当前Pn为比例基准，默认光照持续跟随', () => {
  const slave = device();
  for (const capacity of [25, 75, 50, 1, 100, 25.123]) {
    const oldPn = raw(slave, 30073), oldQ = raw(slave, 30079);
    setRatedCapacity(slave, capacity);
    assert.equal(ratedCapacity(slave), capacity);
    assert.equal(slave.model.dc_available_kw, capacity);
    assert.equal(raw(slave, 30079), Math.max(1, Math.min(raw(slave, 30077), Math.round(oldQ * capacity * 1000 / oldPn))));
    assert.ok(raw(slave, 30075) >= raw(slave, 30073));
    assert.ok(raw(slave, 30077) >= raw(slave, 30075));
  }
});

test('默认直流输入容差内跟随，用户自定义直流输入保留', () => {
  for (const [dc, expected] of [[50, 25], [50 + 1e-9, 25], [51.02, 51.02], [0, 0], [12, 12]]) {
    const slave = device(); slave.model.dc_available_kw = dc;
    setRatedCapacity(slave, 25);
    assert.equal(slave.model.dc_available_kw, expected);
  }
});

test('空值、越界、非有限数值与超出0.001精度的输入均以中文错误原子拒绝', () => {
  for (const input of ['', ' ', null, undefined, false, [], {}, NaN, Infinity, -Infinity, 'bad', 0.999, 100.001, 12.3456]) {
    const slave = device(); const before = structuredClone(slave);
    assert.throws(() => setRatedCapacity(slave, input), /额定容量.*(数字|范围|精度)/);
    assert.deepEqual(slave, before);
  }
});

test('缺失或非法现有能力与初值不会产生部分更新', () => {
  const corruptions = [
    slave => slave.registers.splice(2, 1),
    slave => slave.registers.push(structuredClone(slave.registers[0])),
    slave => { delete slave.registers[0].source; },
    slave => { slave.registers[0].source.value = 0; },
    slave => { slave.registers[1].source.value = 49999; },
    slave => { slave.registers[2].source.value = 100001; },
    slave => { slave.registers[2].source.value = 54999; },
    slave => { slave.registers[3].source.value = 0; },
    slave => { slave.registers[4].source.value = 1; },
    slave => { slave.registers[4].source.value = -60001; },
    slave => { slave.registers[5].source.value = 55001; },
    slave => { slave.registers[6].source.value = 551; },
    slave => { slave.registers[7].source.value = 55001; },
    slave => { slave.registers[7].source.value = -1; },
    slave => { slave.registers[7].source.value = NaN; },
    slave => { slave.registers[7].source.value = 1.5; },
    slave => { slave.registers[7].source.kind = 'sine'; },
    slave => { slave.registers[7].source.kind = false; },
    slave => { slave.registers[0].function = '0x04'; },
    slave => { slave.registers[0].type = 'UINT16'; },
    slave => { slave.registers[0].access = 'readwrite'; },
    slave => { slave.registers[0].word_order = 'LH'; },
  ];
  for (const corrupt of corruptions) {
    const slave = device(); corrupt(slave); const before = structuredClone(slave);
    assert.throws(() => setRatedCapacity(slave, 25), /寄存器|能力|初值|额定/);
    assert.deepEqual(slave, before);
  }
});

test('只更新当前从站，模板与其他设备及非容量寄存器保持独立', () => {
  const template = device(); const first = structuredClone(template), second = structuredClone(template);
  first.device_id = 2; second.device_id = 3;
  first.registers.push({ function: '0x03', address: 32080, source: { kind: 'manual', value: 12000 } });
  const templateBefore = structuredClone(template), secondBefore = structuredClone(second);
  setRatedCapacity(first, 10);
  assert.equal(ratedCapacity(first), 10);
  assert.deepEqual(template, templateBefore);
  assert.deepEqual(second, secondBefore);
  assert.equal(raw(first, 32080), 12000);
});
