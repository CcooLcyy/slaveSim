import test from 'node:test';
import assert from 'node:assert/strict';
import { createRuntimeWorkspace } from '../internal/web/static/runtime-workspace.mjs';

// DOM 替身只验证工作区归属、节点持久性和操作；布局由浏览器检查。
class Node {
  constructor(tag, ownerDocument) {
    this.tagName = tag; this.ownerDocument = ownerDocument; this.children = [];
    this.dataset = {}; this.value = ''; this.textContent = ''; this.hidden = false;
  }
  append(...nodes) {
    this.children.push(...nodes);
    for (const node of nodes) if (typeof node === 'object') node.parent = this;
  }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  setAttribute(key, value) { this[key] = value; }
  removeAttribute(key) { delete this[key]; }
  remove() { this.parent.children = this.parent.children.filter(node => node !== this); }
}

const all = node => [node, ...node.children.filter(child => typeof child === 'object').flatMap(all)];
const find = (node, predicate) => all(node).find(predicate);
const hasClass = (node, name) => (node.className || '').split(' ').includes(name);
const classes = (node, name) => all(node).filter(item => hasClass(item, name));
const busCard = (root, name) => classes(root, 'bus-workspace').find(card => find(card, node => node.tagName === 'h2' && node.textContent === name));
const busButton = (root, name) => classes(root, 'bus-choice').find(button => find(button, node => node.tagName === 'strong' && node.textContent === name));
const device = (bus, id) => classes(bus, 'runtime-device').find(node => node.dataset.deviceId === String(id));
const field = (root, name) => find(root, node => node.name === name);
const manualInput = root => find(root, node => node.tagName === 'input' && node.type === 'text');
const setButton = root => find(root, node => node.tagName === 'button' && node.textContent === '设置');

function fixture(options = {}) {
  const document = { activeElement: null };
  document.createElement = tag => new Node(tag, document);
  document.createElementNS = (_, tag) => new Node(tag, document);
  const root = new Node('div', document);
  return { root, document, workspace: createRuntimeWorkspace(root, options) };
}

function slave(id, modeled = true, active = 50) {
  return {
    device_id: id,
    ...(modeled ? { model: {
      kind: 'huawei_v3', active_kw: active, target_active_kw: active,
      reactive_kvar: 0, target_reactive_kvar: 0, power_factor: 1,
      day_energy_kwh: 1, total_energy_kwh: 100, status: '并网',
      scenario: { dc_available_kw: 51.02, grid_line_voltage_v: 400, grid_frequency_hz: 50, ambient_temperature_c: 25, efficiency: 0.98 },
    } } : {}),
    registers: [{ function: '0x03', address: 40125, type: 'uint16', tag: `设备 ${id} 限功率`, value: active * 10, raw: '01 F4', writable: true }],
  };
}
function state(active = 50, second = '2026-10-11T10:00:00Z') {
  return { now: second, buses: [
    { name: 'Huawei-V3', link_path: '/serial/huawei', slave_path: '/dev/pts/1', slaves: [slave(1, true, active), slave(2, true, active)] },
    { name: 'RS485-2', link_path: '/serial/tty2', slave_path: '/dev/pts/2', slaves: [slave(1, false, active)] },
  ] };
}

test('两个总线分别关联自己的设备、模型和寄存器，同站号不跨总线混合', () => {
  const { root, workspace } = fixture(); workspace.update(state());
  const huawei = busCard(root, 'Huawei-V3'); const other = busCard(root, 'RS485-2');
  assert.equal(classes(root, 'bus-workspace').length, 2);
  assert.equal(classes(huawei, 'runtime-device').length, 2);
  assert.equal(classes(other, 'runtime-device').length, 1);
  for (const id of [1, 2]) {
    const entry = device(huawei, id);
    assert.equal(classes(entry, 'huawei-card').length, 1);
    assert.ok(find(classes(entry, 'huawei-card')[0], node => node.textContent.includes(`站号 ${id}`)));
    assert.ok(find(entry, node => node.tagName === 'summary' && node.textContent.includes(`站号 ${id}`)));
    assert.equal(classes(entry, 'runtime-registers').length, 1);
  }
  assert.equal(classes(other, 'huawei-card').length, 0);
  assert.ok(find(other, node => node.textContent === 'Modbus 设备 · 站号 1'));
  assert.equal(huawei.hidden, false); assert.equal(other.hidden, true);
});

test('切换总线只改变可见性和选中状态，保留设备控制及场景草稿节点', () => {
  const selected = [];
  const { root, workspace } = fixture({ onSelect: name => selected.push(name) }); workspace.update(state());
  const huawei = busCard(root, 'Huawei-V3'); const other = busCard(root, 'RS485-2');
  const entry = device(huawei, 2); const percent = field(entry, 'percent'); const solar = field(entry, 'dc_available_kw');
  percent.value = '37.5'; solar.value = '12'; solar.oninput();
  busButton(root, 'RS485-2').onclick();
  assert.equal(huawei.hidden, true); assert.equal(other.hidden, false);
  assert.equal(busButton(root, 'RS485-2')['aria-pressed'], 'true');
  assert.equal(busButton(root, 'Huawei-V3')['aria-pressed'], 'false');
  workspace.update(state(25, '2026-10-11T10:00:02Z'));
  busButton(root, 'Huawei-V3').onclick();
  assert.equal(busCard(root, 'Huawei-V3'), huawei);
  assert.equal(field(entry, 'percent'), percent); assert.equal(percent.value, '37.5');
  assert.equal(field(entry, 'dc_available_kw'), solar); assert.equal(solar.value, '12');
  assert.equal(selected.at(-1), 'Huawei-V3');
});

test('轮询更新反馈和趋势，保留聚焦手动输入以及寄存器展开状态', () => {
  const { root, document, workspace } = fixture(); workspace.update(state());
  const entry = device(busCard(root, 'Huawei-V3'), 1);
  const registers = classes(entry, 'runtime-registers')[0]; registers.open = true;
  const input = manualInput(entry); input.value = '375'; document.activeElement = input;
  const percent = field(entry, 'percent');
  workspace.update(state(25, '2026-10-11T10:00:02Z'));
  assert.equal(manualInput(entry), input); assert.equal(input.value, '375');
  assert.equal(classes(entry, 'runtime-registers')[0], registers); assert.equal(registers.open, true);
  assert.equal(field(entry, 'percent'), percent);
  assert.ok(find(entry, node => node.textContent === '25.00 kW'));
  assert.ok(find(entry, node => node.textContent === '250'));
  assert.equal(find(entry, node => node.dataset.series === 'actual').points, '0.0,20.0 320.0,70.0');
  document.activeElement = null;
  workspace.update(state(20, '2026-10-11T10:00:04Z'));
  assert.equal(input.value, '200');
});

test('移除选中的总线后回退到剩余总线，并删除不再存在的设备', () => {
  const { root, workspace } = fixture(); workspace.update(state());
  busButton(root, 'RS485-2').onclick();
  const next = state(); next.buses = [next.buses[0]]; next.buses[0].slaves = [next.buses[0].slaves[1]];
  workspace.update(next);
  assert.equal(busCard(root, 'RS485-2'), undefined); assert.equal(busButton(root, 'RS485-2'), undefined);
  const huawei = busCard(root, 'Huawei-V3');
  assert.equal(huawei.hidden, false); assert.equal(busButton(root, 'Huawei-V3')['aria-pressed'], 'true');
  assert.equal(device(huawei, 1), undefined); assert.ok(device(huawei, 2));
});

test('空态、断线及配置重建可见，重建后不保留旧草稿', () => {
  const { root, workspace } = fixture();
  workspace.update({ buses: [] });
  assert.equal(classes(root, 'runtime-empty')[0].hidden, false);
  assert.ok(classes(root, 'runtime-empty')[0].textContent.includes('RS485 配置'));
  workspace.setError(); assert.ok(classes(root, 'runtime-empty')[0].textContent.includes('失败'));
  workspace.update(state());
  const old = field(device(busCard(root, 'Huawei-V3'), 1), 'dc_available_kw'); old.value = '12'; old.oninput();
  workspace.setError(); assert.ok(find(root, node => node.textContent.includes('反馈可能过期')));
  workspace.reset(); assert.equal(classes(root, 'bus-workspace').length, 0);
  workspace.update(state());
  const current = field(device(busCard(root, 'Huawei-V3'), 1), 'dc_available_kw');
  assert.notEqual(current, old); assert.equal(current.value, '51.02');
});

test('手动设置向正确总线和站号提交寄存器原始值，成功后请求刷新', async () => {
  const calls = []; let applied = 0;
  const { root, workspace } = fixture({ fetch: async (url, options) => {
    calls.push({ url, options, body: JSON.parse(options.body) }); return { ok: true, json: async () => ({ ok: true }) };
  }, onApplied: () => applied++ }); workspace.update(state());
  for (const [name, id, value] of [['Huawei-V3', 2, '375'], ['RS485-2', 1, '625']]) {
    busButton(root, name).onclick(); const entry = device(busCard(root, name), id);
    manualInput(entry).value = value; await setButton(entry).onclick();
  }
  assert.deepEqual(calls.map(call => call.body), [
    { bus: 'Huawei-V3', device_id: 2, function: '0x03', address: 40125, value: 375 },
    { bus: 'RS485-2', device_id: 1, function: '0x03', address: 40125, value: 625 },
  ]);
  assert.ok(calls.every(call => call.url === '/api/manual' && call.options.method === 'POST'));
  assert.equal(applied, 2);
});

test('手动设置失败保留输入并显示文本错误，非法输入不会发送请求', async t => {
  t.mock.method(console, 'error', () => {});
  let calls = 0; let applied = 0;
  const { root, workspace } = fixture({ fetch: async () => {
    calls++; return { ok: false, status: 400, json: async () => ({ error: '不可写 <x>' }) };
  }, onApplied: () => applied++ }); workspace.update(state());
  const entry = device(busCard(root, 'RS485-2'), 1); const input = manualInput(entry); const button = setButton(entry);
  input.value = '375'; await button.onclick();
  assert.equal(input.value, '375'); assert.equal(button.disabled, false);
  assert.ok(find(entry, node => node.textContent === '设置失败：不可写 <x>'));
  assert.equal(applied, 0);
  for (const value of ['', ' ', 'NaN', 'Infinity']) { input.value = value; await button.onclick(); }
  assert.equal(calls, 1); assert.ok(find(entry, node => node.textContent === '请输入有效数字'));
});

test('手动请求成功不清除请求期间继续编辑的新草稿', async () => {
  let complete; const calls = [];
  const { root, document, workspace } = fixture({ fetch: (url, options) => {
    calls.push(JSON.parse(options.body));
    return new Promise(resolve => { complete = resolve; });
  } });
  workspace.update(state());
  const entry = device(busCard(root, 'RS485-2'), 1);
  const input = manualInput(entry); document.activeElement = input;
  input.value = '375'; input.oninput();
  const pending = setButton(entry).onclick();
  input.value = '625'; input.oninput();
  complete({ ok: true, json: async () => ({ ok: true }) }); await pending;
  document.activeElement = null;
  workspace.update(state(37.5, '2026-10-11T10:00:02Z'));
  assert.equal(calls[0].value, 375);
  assert.equal(manualInput(entry), input); assert.equal(input.value, '625');
  assert.ok(find(entry, node => node.textContent === '375'));
});
