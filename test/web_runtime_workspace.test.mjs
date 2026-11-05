import test from 'node:test';
import assert from 'node:assert/strict';
import { createRuntimeWorkspace } from '../internal/web/static/runtime-workspace.mjs';

// DOM 替身只验证工作区归属、节点持久性和操作；布局由浏览器检查。
class Node {
  constructor(tag, ownerDocument) {
    this.tagName = tag; this.ownerDocument = ownerDocument; this.children = [];
    this.dataset = {}; this.value = ''; this.textContent = ''; this.hidden = false; this.open = false;
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
const modelCard = entry => classes(entry, 'huawei-card')[0];
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

// 清空后必须移除旧设备反馈，明确显示零台，再添加时只显示新设备。
test('运行状态清空设备显示零台并移除旧反馈，之后可重新添加', () => {
  const { root, workspace } = fixture();
  workspace.update(state());
  const cleared = state(); cleared.buses[0].slaves = []; cleared.buses = cleared.buses.slice(0, 1);
  workspace.update(cleared);
  const bus = busCard(root, 'Huawei-V3');
  assert.equal(classes(root, 'runtime-device').length, 0);
  const empty = classes(bus, 'bus-empty-devices')[0];
  assert.equal(empty.hidden, false);
  assert.match(empty.textContent, /0台/);
  workspace.update(state());
  assert.equal(empty.hidden, true);
  assert.equal(classes(root, 'runtime-device').length, 3);
  workspace.update({ now: 1, buses: [] });
  assert.equal(classes(root, 'bus-workspace').length, 0);
  assert.equal(classes(root, 'runtime-empty')[0].hidden, false);
});

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

test('实际设备优先展示，复制按钮在刷新后复制最新设备和固定配置路径', async () => {
  const copied = [];
  const { root, workspace } = fixture({ copyText: async value => copied.push(value) });
  workspace.update(state());
  const connection = classes(busCard(root, 'Huawei-V3'), 'bus-connection')[0];
  const rows = classes(connection, 'bus-path-row');
  assert.ok(rows[0].children.some(node => node.textContent === '实际设备'));
  assert.equal(classes(rows[0], 'bus-path-value')[0].textContent, '/dev/pts/1');
  assert.equal(classes(rows[1], 'bus-path-value')[0].textContent, '/serial/huawei');
  const buttons = classes(connection, 'copy-path');
  await buttons[0].onclick(); await buttons[1].onclick();
  const next = state(); next.buses[0].slave_path = '/dev/pts/12'; workspace.update(next);
  assert.equal(classes(busCard(root, 'Huawei-V3'), 'bus-connection')[0], connection);
  await buttons[0].onclick(); await buttons[1].onclick();
  assert.deepEqual(copied, ['/dev/pts/1', '/serial/huawei', '/dev/pts/12', '/serial/huawei']);
  assert.ok(classes(connection, 'bus-path-hint')[0].textContent.includes('serial.device'));
  assert.equal(classes(connection, 'copy-path-status')[0].textContent, '固定配置路径已复制');
});

test('路径缺失时禁用复制，复制失败显示可手动复制的提示', async t => {
  t.mock.method(console, 'error', () => {});
  const { root, workspace } = fixture({ copyText: async () => { throw new Error('权限拒绝'); } });
  workspace.update(state());
  const connection = classes(busCard(root, 'Huawei-V3'), 'bus-connection')[0];
  const buttons = classes(connection, 'copy-path');
  await buttons[0].onclick();
  assert.equal(buttons[0].disabled, false);
  assert.ok(classes(connection, 'copy-path-status')[0].textContent.includes('请手动选择路径复制'));
  const next = state(); next.buses[0].slave_path = ''; next.buses[0].link_path = '';
  workspace.update(next);
  const unavailable = classes(busCard(root, 'Huawei-V3'), 'copy-path');
  assert.ok(unavailable.every(button => button.disabled));
});

test('两个总线分别关联自己的设备、模型和寄存器，同站号不跨总线混合', () => {
  const { root, workspace } = fixture(); workspace.update(state());
  const huawei = busCard(root, 'Huawei-V3'); const other = busCard(root, 'RS485-2');
  assert.equal(classes(root, 'bus-workspace').length, 2);
  assert.equal(classes(huawei, 'runtime-device').length, 2);
  assert.equal(classes(other, 'runtime-device').length, 1);
  for (const id of [1, 2]) {
    const entry = device(huawei, id);
    const header = classes(entry, 'device-header')[0];
    assert.equal(classes(entry, 'huawei-card').length, 1);
    assert.equal(entry.children[0], header);
    assert.ok(find(header, node => node.textContent === `站号 ${id}`));
    assert.ok(find(header, node => node.textContent.includes('Huawei V3')));
    assert.equal(classes(header, 'huawei-compact').length, 1);
    assert.equal(classes(header, 'huawei-status').length, 1);
    assert.equal(classes(modelCard(entry), 'device-identity').length, 0);
    assert.ok(find(modelCard(entry).children[0], node => node.textContent === '行为模型'));
    assert.equal(classes(entry, 'runtime-registers').length, 1);
    assert.equal(classes(entry, 'runtime-registers')[0].children[0].textContent, '寄存器与手动设值 · 1 项');
    assert.equal(classes(entry, 'runtime-registers')[0].open, false);
  }
  assert.equal(classes(other, 'huawei-card').length, 0);
  assert.ok(find(other, node => node.textContent === 'Modbus 设备 · 站号 1'));
  assert.equal(huawei.hidden, false); assert.equal(other.hidden, true);
});

test('共用设备标题在两个区域收起时刷新反馈和断线提示，不改变独立展开状态', () => {
  const { root, workspace } = fixture(); workspace.update(state());
  const entry = device(busCard(root, 'Huawei-V3'), 1);
  const header = classes(entry, 'device-header')[0];
  const model = modelCard(entry); const registers = classes(entry, 'runtime-registers')[0];
  assert.equal(model.open, false); assert.equal(registers.open, false);
  workspace.setError();
  assert.ok(find(header, node => node.textContent.includes('反馈可能过期')));
  workspace.update(state(25, '2026-10-11T10:00:02Z'));
  assert.equal(classes(entry, 'device-header')[0], header);
  assert.ok(find(header, node => node.textContent === '25.00 kW'));
  assert.ok(find(header, node => node.textContent === '并网'));
  assert.equal(model.open, false); assert.equal(registers.open, false);
  registers.open = true;
  assert.equal(model.open, false);
  model.open = true; registers.open = false;
  workspace.update(state(20, '2026-10-11T10:00:04Z'));
  assert.equal(model.open, true); assert.equal(registers.open, false);
});

test('同站号切换普通设备和模型设备不会残留标题或丢失寄存器草稿', () => {
  const { root, workspace } = fixture(); workspace.update(state());
  const entry = device(busCard(root, 'Huawei-V3'), 1);
  const header = classes(entry, 'device-header')[0];
  const registers = classes(entry, 'runtime-registers')[0]; registers.open = true;
  const input = manualInput(entry); input.value = '375'; input.oninput();
  const next = state(); delete next.buses[0].slaves[0].model;
  workspace.update(next);
  assert.equal(classes(header, 'runtime-device-heading').length, 0);
  assert.equal(classes(header, 'static-device-title')[0].hidden, false);
  assert.equal(modelCard(entry), undefined);
  assert.equal(registers.open, true); assert.equal(manualInput(entry), input); assert.equal(input.value, '375');
  workspace.update(state());
  assert.equal(classes(header, 'runtime-device-heading').length, 1);
  assert.equal(classes(header, 'static-device-title')[0].hidden, true);
  assert.equal(modelCard(entry).open, false); assert.equal(registers.open, true);
  assert.equal(input.value, '375');
});

test('每台设备的行为模型独立展开，收起轮询不发送控制，展开写入仍对应原站号', async () => {
  const calls = [];
  const { root, workspace } = fixture({ fetch: async (url, options) => {
    calls.push({ url, body: JSON.parse(options.body) }); return { ok: true, json: async () => ({ ok: true }) };
  } });
  workspace.update(state());
  const bus = busCard(root, 'Huawei-V3');
  const first = modelCard(device(bus, 1)); const second = modelCard(device(bus, 2));
  assert.equal(first.tagName, 'details'); assert.equal(second.tagName, 'details');
  assert.equal(first.open, false); assert.equal(second.open, false);
  first.open = true;
  assert.equal(second.open, false);
  second.open = true; first.open = false;
  workspace.update(state(25, '2026-10-11T10:00:02Z'));
  assert.equal(modelCard(device(bus, 1)), first); assert.equal(modelCard(device(bus, 2)), second);
  assert.equal(first.open, false); assert.equal(second.open, true);
  assert.equal(calls.length, 0);
  await find(second, node => node.textContent === '50% 限功率').onclick();
  assert.equal(calls.length, 1); assert.equal(calls[0].url, '/api/manual');
  assert.equal(calls[0].body.bus, 'Huawei-V3'); assert.equal(calls[0].body.device_id, 2);
  assert.equal(calls[0].body.value, 500);
  assert.equal(first.open, false); assert.equal(second.open, true);
});

test('切换总线只改变可见性和选中状态，保留设备控制及场景草稿节点', () => {
  const selected = [];
  const { root, workspace } = fixture({ onSelect: name => selected.push(name) }); workspace.update(state());
  const huawei = busCard(root, 'Huawei-V3'); const other = busCard(root, 'RS485-2');
  const entry = device(huawei, 2); const percent = field(entry, 'percent'); const solar = field(entry, 'dc_available_kw');
  const firstModel = modelCard(device(huawei, 1)); const secondModel = modelCard(entry);
  secondModel.open = true;
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
  assert.equal(modelCard(entry), secondModel); assert.equal(secondModel.open, true);
  assert.equal(modelCard(device(huawei, 1)), firstModel); assert.equal(firstModel.open, false);
  assert.equal(selected.at(-1), 'Huawei-V3');
});

test('轮询更新反馈和趋势，保留聚焦手动输入以及寄存器展开状态', () => {
  const { root, document, workspace } = fixture(); workspace.update(state());
  const entry = device(busCard(root, 'Huawei-V3'), 1);
  const model = modelCard(entry); model.open = true;
  const registers = classes(entry, 'runtime-registers')[0]; registers.open = true;
  const input = manualInput(entry); input.value = '375'; document.activeElement = input;
  const percent = field(entry, 'percent');
  workspace.update(state(25, '2026-10-11T10:00:02Z'));
  assert.equal(manualInput(entry), input); assert.equal(input.value, '375');
  assert.equal(classes(entry, 'runtime-registers')[0], registers); assert.equal(registers.open, true);
  assert.equal(field(entry, 'percent'), percent);
  assert.equal(modelCard(entry), model); assert.equal(model.open, true);
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
