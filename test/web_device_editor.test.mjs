import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { newBus, newSlave } from '../internal/web/static/config-model.mjs';

const configuredBus = (buses = []) => ({ ...newBus(buses), slaves: [newSlave()] });

class Node {
  constructor(tag) { this.tagName = tag; this.children = []; this.textContent = ''; this.value = ''; this.dataset = {}; }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = [...nodes]; }
  setAttribute(key, value) { this[key] = value; }
}
const all = node => [node, ...node.children.filter(child => typeof child === 'object').flatMap(all)];
const huawei = JSON.parse(readFileSync(new URL('../configs/inverters/huawei-v3-model.json', import.meta.url), 'utf8')).buses[0].slaves[0];
const catalog = [{ id: 'huawei_v3', name: '华为逆变器（V3，行为仿真）', slave: huawei }];
let serial = 0;

async function editor(fetch, config = { buses: [configuredBus()] }) {
  const ids = new Map();
  const document = { getElementById: id => { if (!ids.has(id)) ids.set(id, new Node(id)); return ids.get(id); }, createElement: tag => new Node(tag) };
  const calls = [];
  const old = { document: globalThis.document, window: globalThis.window, fetch: globalThis.fetch };
  globalThis.document = document;
  globalThis.window = { addEventListener() {}, dispatchEvent() {}, confirm: () => true };
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    if (url === '/api/config') return options?.method === 'PUT' ? { ok: true, json: async () => ({}) } : { ok: true, json: async () => config };
    return fetch(url, options);
  };
  await import(`../internal/web/static/config-editor.mjs?test=${++serial}`);
  document.getElementById('show-config').onclick();
  await new Promise(resolve => setImmediate(resolve));
  return { document, calls, root: document.getElementById('config-buses'), restore: () => Object.assign(globalThis, old) };
}
const select = root => all(root).find(node => node.name === 'device-type');
const add = root => all(root).find(node => node.textContent === '添加设备');

test('设备预置加载失败仍可新增自定义并保存旧配置，不触发自动应用', async () => {
  const ui = await editor(async () => { throw new TypeError('Failed to fetch'); });
  try {
    assert.equal(select(ui.root).value, '__custom_modbus__');
    assert.match(ui.document.getElementById('device-template-status').textContent, /连接失败/);
    add(ui.root).onclick();
    assert.equal(ui.calls.filter(call => call.options?.method === 'PUT').length, 0);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body);
    assert.equal(saved.buses[0].slaves.length, 2);
    assert.equal(saved.buses[0].slaves[0].device_id, 1);
    assert.equal(saved.buses[0].slaves[1].device_id, 2);
    assert.equal(saved.buses[0].slaves[1].model, undefined);
  } finally { ui.restore(); }
});

test('模板异步到达不重建输入或覆盖未保存草稿，预置仅加入所选总线', async () => {
  let finish;
  const config = { buses: [configuredBus(), { ...configuredBus([configuredBus()]), slaves: [{ ...configuredBus().slaves[0], device_id: 2 }] }] };
  const ui = await editor(async () => new Promise(resolve => { finish = resolve; }), config);
  try {
    const nameInput = all(ui.root).find(node => node.value === 'RS485-1');
    nameInput.value = '尚未保存的总线'; nameInput.oninput();
    finish({ ok: true, json: async () => catalog });
    await new Promise(resolve => setImmediate(resolve));
    assert.ok(all(ui.root).includes(nameInput));
    assert.equal(nameInput.value, '尚未保存的总线');
    select(ui.root).value = 'huawei_v3'; select(ui.root).onchange(); add(ui.root).onclick();
    assert.equal(ui.calls.filter(call => call.options?.method === 'PUT').length, 0);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body);
    assert.equal(saved.buses[0].name, '尚未保存的总线');
    assert.equal(saved.buses[0].slaves[1].device_id, 2);
    assert.equal(saved.buses[0].slaves[1].model.kind, 'huawei_v3');
    assert.equal(saved.buses[1].slaves.length, 1);
    assert.deepEqual(config.buses[0].slaves.length, 1);
  } finally { ui.restore(); }
});

test('已删除总线的旧按钮不能把设备加到另一条总线', async () => {
  const config = { buses: [configuredBus(), configuredBus([configuredBus()])] };
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), config);
  try {
    const stale = add(ui.root);
    all(ui.root).find(node => node.textContent === '删除总线').onclick();
    stale.onclick();
    assert.match(ui.document.getElementById('config-status').textContent, /目标总线已移除/);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body);
    assert.equal(saved.buses.length, 1);
    assert.equal(saved.buses[0].name, 'RS485-2');
    assert.equal(saved.buses[0].slaves.length, 1);
  } finally { ui.restore(); }
});

test('总线站号用满时显示中文错误，不添加248号或应用配置', async () => {
  const bus = configuredBus();
  bus.slaves = Array.from({ length: 247 }, (_, index) => ({ ...configuredBus().slaves[0], device_id: index + 1 }));
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), { buses: [bus] });
  try {
    add(ui.root).onclick();
    assert.match(ui.document.getElementById('config-status').textContent, /1～247已用满/);
    assert.equal(ui.calls.filter(call => call.options?.method === 'PUT').length, 0);
  } finally { ui.restore(); }
});

test('失败后重试恢复预置选择，同时保留输入中的旧草稿', async () => {
  let attempt = 0;
  const ui = await editor(async () => { if (!attempt++) throw new Error(); return { ok: true, json: async () => catalog }; });
  try {
    const nameInput = all(ui.root).find(node => node.value === 'RS485-1');
    nameInput.value = '重试期间保留'; nameInput.oninput();
    await ui.document.getElementById('retry-device-templates').onclick();
    assert.ok(all(ui.root).includes(nameInput));
    assert.equal(nameInput.value, '重试期间保留');
    assert.ok(select(ui.root).children.some(node => node.value === 'huawei_v3'));
    assert.equal(ui.document.getElementById('retry-device-templates').hidden, true);
  } finally { ui.restore(); }
});

const capacityInput = root => all(root).find(node => node.name === 'rated_capacity_kw');
const labelledInput = (root, text) => all(root).find(node => node.tagName === 'label' && node.children.some(child => child.textContent === text))?.children.find(node => node.tagName === 'input');

const cards = (root, className) => all(root).filter(node => node.className?.split(' ').includes(className));
const summaryText = node => all(node.children.find(child => child.tagName === 'summary')).map(child => child.textContent).join(' ');

test('新建总线默认为空，保存提示添加从站，手动添加首台设备后站号为1并可保存', async () => {
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }));
  try {
    ui.document.getElementById('add-bus').onclick();
    const bus = cards(ui.root, 'bus-editor').at(-1);
    assert.equal(bus.open, true);
    assert.equal(cards(bus, 'slave-editor').length, 0);
    assert.match(summaryText(bus), /0 个从站/);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    assert.equal(ui.calls.filter(call => call.options?.method === 'PUT').length, 0);
    assert.match(ui.document.getElementById('config-status').textContent, /至少添加一个从站/);
    add(bus).onclick();
    const station = cards(cards(ui.root, 'bus-editor').at(-1), 'slave-editor')[0];
    assert.equal(station.open, true);
    assert.match(summaryText(station), /从站 1/);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body);
    assert.equal(saved.buses[1].slaves.length, 1);
    assert.equal(saved.buses[1].slaves[0].device_id, 1);
    assert.equal(saved.buses[0].slaves.length, 1);
  } finally { ui.restore(); }
});

test('从空总线添加华为设备时转换效率显示1，已有设备效率读取和保存均保留', async () => {
  const existing = structuredClone(huawei); existing.model.efficiency = 0.93;
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), { buses: [{ ...newBus(), slaves: [existing] }] });
  try {
    assert.equal(Number(labelledInput(ui.root, '转换效率（0～1，不含0）').value), 0.93);
    ui.document.getElementById('add-bus').onclick();
    const bus = cards(ui.root, 'bus-editor').at(-1);
    select(bus).value = 'huawei_v3'; select(bus).onchange(); add(bus).onclick();
    const updatedBus = cards(ui.root, 'bus-editor').at(-1);
    assert.equal(Number(labelledInput(updatedBus, '转换效率（0～1，不含0）').value), 1);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body);
    assert.equal(saved.buses[0].slaves[0].model.efficiency, 0.93);
    assert.equal(saved.buses[1].slaves[0].model.efficiency, 1);
    assert.equal(saved.buses[1].slaves[0].device_id, 1);
    assert.equal(existing.model.efficiency, 0.93);
  } finally { ui.restore(); }
});

test('配置默认展开总线并收起从站，标题展示总线与设备摘要', async () => {
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), { buses: [{ ...configuredBus(), slaves: [huawei] }] });
  try {
    const bus = cards(ui.root, 'bus-editor')[0]; const slave = cards(ui.root, 'slave-editor')[0];
    assert.equal(bus.tagName, 'details'); assert.equal(bus.open, true);
    assert.equal(slave.tagName, 'details'); assert.equal(slave.open, false);
    assert.match(summaryText(bus), /RS485-1/); assert.match(summaryText(bus), /1 个从站/);
    assert.match(summaryText(bus), /\/srv\/serial-sim\/tty0/);
    assert.match(summaryText(slave), /从站 1/); assert.match(summaryText(slave), /华为逆变器/);
    assert.match(summaryText(slave), new RegExp(`${huawei.registers.length} (项|个寄存器)`));
  } finally { ui.restore(); }
});

test('全部展开收起不改草稿状态或输入节点，且不强制展开寄存器', async () => {
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }));
  try {
    const initialStatus = ui.document.getElementById('config-status').textContent;
    const input = labelledInput(ui.root, '总线名称');
    const registers = cards(ui.root, 'register-group')[0];
    ui.document.getElementById('expand-config').onclick();
    assert.equal(cards(ui.root, 'bus-editor')[0].open, true);
    assert.equal(cards(ui.root, 'slave-editor')[0].open, true);
    assert.equal(registers.open, false);
    ui.document.getElementById('collapse-config').onclick();
    assert.equal(cards(ui.root, 'bus-editor')[0].open, false);
    assert.equal(cards(ui.root, 'slave-editor')[0].open, false);
    assert.equal(ui.document.getElementById('config-status').textContent, initialStatus);
    input.value = '未保存名称'; input.oninput();
    ui.document.getElementById('expand-config').onclick();
    assert.ok(all(ui.root).includes(input)); assert.equal(input.value, '未保存名称');
    assert.equal(ui.calls.filter(call => call.options?.method === 'PUT').length, 0);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body);
    assert.equal(saved.buses[0].name, '未保存名称');
    assert.deepEqual(Object.keys(saved.buses[0]).sort(), ['link', 'name', 'slaves']);
  } finally { ui.restore(); }
});

test('新增设备和总线自动展开，增删重绘保留其他节点折叠状态和草稿', async () => {
  const config = { buses: [configuredBus(), configuredBus([configuredBus()])] };
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), config);
  try {
    cards(ui.root, 'bus-editor')[1].open = false;
    cards(ui.root, 'slave-editor')[0].open = true;
    const input = labelledInput(ui.root, '总线名称'); input.value = '编辑后名称'; input.oninput();
    add(ui.root).onclick();
    assert.equal(cards(ui.root, 'bus-editor')[1].open, false);
    assert.equal(cards(ui.root, 'slave-editor')[0].open, true);
    assert.equal(cards(ui.root, 'slave-editor')[1].open, true);
    assert.match(summaryText(cards(ui.root, 'bus-editor')[0]), /2 个从站/);
    all(cards(ui.root, 'slave-editor')[1]).find(node => node.textContent === '删除从站').onclick();
    assert.equal(cards(ui.root, 'bus-editor')[1].open, false);
    assert.equal(cards(ui.root, 'slave-editor')[0].open, true);
    assert.equal(labelledInput(ui.root, '总线名称').value, '编辑后名称');
    ui.document.getElementById('add-bus').onclick();
    assert.equal(cards(ui.root, 'bus-editor').at(-1).open, true);
    assert.equal(cards(cards(ui.root, 'bus-editor').at(-1), 'slave-editor').length, 0);
    assert.equal(cards(ui.root, 'bus-editor')[1].open, false);
  } finally { ui.restore(); }
});

test('重新读取配置恢复默认总线和从站折叠状态', async () => {
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }));
  try {
    ui.document.getElementById('expand-config').onclick();
    cards(ui.root, 'bus-editor')[0].open = false;
    await ui.document.getElementById('reload-config').onclick();
    assert.equal(cards(ui.root, 'bus-editor')[0].open, true);
    assert.equal(cards(ui.root, 'slave-editor')[0].open, false);
  } finally { ui.restore(); }
});

test('编辑名称路径站号立即更新折叠摘要，增删寄存器刷新数量', async () => {
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }));
  try {
    for (const [label, value] of [['总线名称', '现场总线'], ['串口软链接路径', '/srv/serial-sim/tty7'], ['站号（1～247）', '7']]) {
      const input = labelledInput(ui.root, label); input.value = value; input.oninput();
    }
    assert.match(summaryText(cards(ui.root, 'bus-editor')[0]), /现场总线/);
    assert.match(summaryText(cards(ui.root, 'bus-editor')[0]), /\/srv\/serial-sim\/tty7/);
    assert.match(summaryText(cards(ui.root, 'slave-editor')[0]), /从站 7/);
    assert.match(summaryText(cards(ui.root, 'slave-editor')[0]), /自定义 Modbus 设备/);
    all(ui.root).find(node => node.textContent === '新增寄存器').onclick();
    assert.match(summaryText(cards(ui.root, 'slave-editor')[0]), /2 个寄存器/);
    all(ui.root).find(node => node.textContent === '删除寄存器').onclick();
    assert.match(summaryText(cards(ui.root, 'slave-editor')[0]), /1 个寄存器/);
  } finally { ui.restore(); }
});

test('折叠标题中的删除按钮阻止默认切换和冒泡且只移除目标项', async () => {
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), { buses: [configuredBus(), configuredBus([configuredBus()])] });
  try {
    let prevented = 0; let stopped = 0;
    const event = { preventDefault() { prevented++; }, stopPropagation() { stopped++; } };
    all(cards(ui.root, 'slave-editor')[0]).find(node => node.textContent === '删除从站').onclick(event);
    assert.equal(prevented, 1); assert.equal(stopped, 1);
    assert.equal(cards(ui.root, 'bus-editor').length, 2);
    assert.equal(cards(ui.root, 'slave-editor').length, 1);
    assert.match(summaryText(cards(ui.root, 'bus-editor')[0]), /0 个从站/);
    all(cards(ui.root, 'bus-editor')[0]).find(node => node.textContent === '删除总线').onclick(event);
    assert.equal(prevented, 2); assert.equal(stopped, 2);
    assert.equal(cards(ui.root, 'bus-editor').length, 1);
    assert.match(summaryText(cards(ui.root, 'bus-editor')[0]), /RS485-2/);
    assert.equal(cards(ui.root, 'slave-editor').length, 1);
  } finally { ui.restore(); }
});

test('模型参数区配置额定容量并保存关联能力，默认光照随容量更新且重读保留', async () => {
  const station = structuredClone(huawei); station.model.dc_available_kw = 50;
  const config = { buses: [{ ...configuredBus(), slaves: [station] }] };
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), config);
  try {
    const rated = capacityInput(ui.root);
    assert.ok(rated, '额定容量应直接出现在模型参数区');
    assert.equal(Number(rated.value), 50);
    assert.equal(Number(rated.min), 0.001); assert.equal(rated.max, undefined);
    rated.value = '250'; rated.oninput(); rated.onchange();
    assert.equal(Number(labelledInput(ui.root, '太阳可用直流功率（kW）').value), 250);
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body);
    const device = saved.buses[0].slaves[0];
    const raw = address => device.registers.find(reg => reg.address === address).source.value;
    assert.equal(raw(30073), 250000); assert.equal(raw(30075), 275000); assert.equal(raw(30077), 275000);
    assert.equal(raw(40126), 250000); assert.equal(raw(40120), 2500);
    assert.equal(device.model.dc_available_kw, 250);
    Object.assign(config, saved);
    await ui.document.getElementById('reload-config').onclick();
    assert.equal(Number(capacityInput(ui.root).value), 250);
    assert.equal(Number(labelledInput(ui.root, '太阳可用直流功率（kW）').value), 250);
    assert.equal(station.registers.find(reg => reg.address === 30073).source.value, 50000);
  } finally { ui.restore(); }
});

test('未失焦的容量在保存时生效，自定义光照和另一台逆变器不受影响', async () => {
  const first = structuredClone(huawei); first.model.dc_available_kw = 12;
  const second = structuredClone(huawei); second.device_id = 2;
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), { buses: [{ ...configuredBus(), slaves: [first, second] }] });
  try {
    const rated = capacityInput(ui.root); rated.value = '250'; rated.oninput();
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    const saved = JSON.parse(ui.calls.find(call => call.options?.method === 'PUT').options.body).buses[0].slaves;
    assert.equal(saved[0].registers.find(reg => reg.address === 30073).source.value, 250000);
    assert.equal(saved[0].model.dc_available_kw, 12);
    assert.equal(saved[1].registers.find(reg => reg.address === 30073).source.value, 50000);
    assert.equal(saved[1].model.dc_available_kw, second.model.dc_available_kw);
  } finally { ui.restore(); }
});

test('非法容量保留草稿并禁止保存，修正后可以应用', async () => {
  const ui = await editor(async () => ({ ok: true, json: async () => catalog }), { buses: [{ ...configuredBus(), slaves: [huawei] }] });
  try {
    const rated = capacityInput(ui.root);
    for (const value of ['', '0', '-1', '1.0001']) {
      rated.value = value; rated.oninput(); rated.onchange();
      await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
      assert.equal(ui.calls.filter(call => call.options?.method === 'PUT').length, 0);
      assert.equal(rated.value, value);
      assert.match(ui.document.getElementById('config-status').textContent, /额定容量/);
    }
    rated.value = '20'; rated.oninput(); rated.onchange();
    await ui.document.getElementById('config-form').onsubmit({ preventDefault() {} });
    assert.equal(ui.calls.filter(call => call.options?.method === 'PUT').length, 1);
  } finally { ui.restore(); }
});
