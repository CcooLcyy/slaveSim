import test from 'node:test';
import assert from 'node:assert/strict';
import { createHuaweiPanel } from '../internal/web/static/huawei-panel.mjs';

// 使用最小DOM替身验证刷新与操作编排，不依赖浏览器布局实现。
class Node {
  constructor(tag, ownerDocument) { this.tagName = tag; this.ownerDocument = ownerDocument; this.children = []; this.dataset = {}; this.listeners = {}; this.value = ''; this.textContent = ''; this.open = false; }
  append(...nodes) { this.children.push(...nodes); for (const node of nodes) if (typeof node === 'object') node.parent = this; }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  setAttribute(key, value) { this[key] = value; }
  removeAttribute(key) { delete this[key]; }
  addEventListener(event, listener) { this.listeners[event] = listener; }
  remove() { this.parent.children = this.parent.children.filter(child => child !== this); }
}
const document = { createElement: tag => new Node(tag, document), createElementNS: (namespace, tag) => new Node(tag, document) };
const state = (active = 50, dc = 51.02, maximum = 55) => ({ buses: [{ name: 'Huawei-V3', slaves: [{ device_id: 1, model: {
  kind: 'huawei_v3', active_kw: active, target_active_kw: active, reactive_kvar: 0, target_reactive_kvar: 0, power_factor: 1,
  day_energy_kwh: 0, total_energy_kwh: 54321, status: '并网', active_command: 40125, reactive_command: 40122,
  scenario: { dc_available_kw: dc, grid_line_voltage_v: 400, grid_frequency_hz: 50, ambient_temperature_c: 25, efficiency: 0.98 },
}, registers: [{ address: 30075, value: maximum * 1000 }] }] }] });
const find = (node, predicate) => [node, ...node.children.filter(child => typeof child === 'object').flatMap(child => all(child))].find(predicate);
const all = node => [node, ...node.children.filter(child => typeof child === 'object').flatMap(all)];
const input = (root, name) => find(root, node => node.name === name);
const hasClass = (node, name) => (node.className || '').split(' ').includes(name);
const modelCard = root => find(root, node => hasClass(node, 'huawei-card'));
const modelSummary = card => card.children.find(node => node.tagName === 'summary');
const compactValues = summary => all(find(summary, node => hasClass(node, 'huawei-compact')))
  .filter(node => hasClass(node, 'huawei-compact-value') || node.dataset.metric)
  .map(node => node.textContent);

test('扰动默认开启，关闭草稿在轮询中保留，提交布尔值并跟随服务端反馈刷新', async () => {
  const calls = [];
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root, { fetch: async (url, options) => { calls.push(JSON.parse(options.body)); return { ok: true, json: async () => ({}) }; } });
  panel.update(state());
  const toggle = input(root, 'disturbance_enabled');
  assert.ok(toggle); assert.equal(toggle.type, 'checkbox'); assert.equal(toggle.checked, true);
  toggle.checked = false; toggle.onchange();
  panel.update(state());
  assert.equal(input(root, 'disturbance_enabled'), toggle); assert.equal(toggle.checked, false);
  await find(root, node => node.dataset.form === 'scenario').onsubmit({ preventDefault() {} });
  assert.equal(calls[0].scenario.disturbance_enabled, false);
  const disabled = state(); disabled.buses[0].slaves[0].model.scenario.disturbance_enabled = false;
  panel.update(disabled); assert.equal(toggle.checked, false);
  input(root, 'dc_available_kw').value = '20'; input(root, 'dc_available_kw').oninput();
  await find(root, node => node.dataset.form === 'scenario').onsubmit({ preventDefault() {} });
  assert.equal(calls[1].scenario.disturbance_enabled, false);
  panel.update(state()); assert.equal(toggle.checked, true);
});

test('行为模型默认收起，摘要保留设备身份、状态和实际P/Q/PF并持续刷新', () => {
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root);
  panel.update(state());
  const card = modelCard(root);
  assert.equal(card.tagName, 'details'); assert.equal(card.open, false);
  const summary = modelSummary(card);
  assert.ok(summary); assert.equal(card.children[0], summary);
  assert.ok(find(summary, node => node.textContent.includes('站号 1')));
  assert.ok(find(summary, node => node.textContent.includes('Huawei V3')));
  assert.ok(find(summary, node => node.textContent === '并网'));
  assert.deepEqual(compactValues(summary), ['50.00 kW', '0.00 kvar', '1.000']);
  assert.equal(summary.onclick, undefined);
  assert.ok(find(summary, node => hasClass(node, 'huawei-toggle')));
  const next = state(25);
  next.buses[0].slaves[0].model.reactive_kvar = -12.34;
  next.buses[0].slaves[0].model.power_factor = -0.955;
  panel.update(next);
  assert.equal(modelCard(root), card); assert.equal(modelSummary(card), summary);
  assert.equal(card.open, false);
  assert.deepEqual(compactValues(summary), ['25.00 kW', '-12.34 kvar', '-0.955']);
});

test('收起和展开只改变模型可见性，不自动提交，展开后原控制和场景仍可使用', async () => {
  const calls = [];
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root, { fetch: async (url, options) => {
    calls.push({ url, body: JSON.parse(options.body) }); return { ok: true, json: async () => ({ ok: true }) };
  } });
  panel.update(state());
  const card = modelCard(root); assert.equal(card.tagName, 'details');
  const percent = input(card, 'percent'); percent.value = '37.5';
  const dc = input(card, 'dc_available_kw'); dc.value = '12'; dc.oninput();
  panel.update(state(25, 40));
  card.open = true; card.open = false; card.open = true;
  assert.equal(calls.length, 0);
  assert.equal(input(card, 'percent'), percent); assert.equal(percent.value, '37.5');
  assert.equal(input(card, 'dc_available_kw'), dc); assert.equal(dc.value, '12');
  await find(card, node => node['aria-label'] === '写入有功百分比（%）').onclick();
  await find(card, node => node.dataset.form === 'scenario').onsubmit({ preventDefault() {} });
  assert.equal(calls[0].url, '/api/manual'); assert.equal(calls[0].body.value, 375);
  assert.equal(calls[1].url, '/api/scenario'); assert.equal(calls[1].body.scenario.dc_available_kw, 12);
  assert.ok(calls.every(call => call.body.device_id === 1 && call.body.bus === 'Huawei-V3'));
  assert.equal(card.open, true);
});

test('模型收起时连接中断仍在摘要提示，恢复反馈不改变收起状态', () => {
  const root = new Node('div', document); const panel = createHuaweiPanel(root);
  panel.update(state());
  const card = modelCard(root); assert.equal(card.tagName, 'details');
  const summary = modelSummary(card);
  panel.setError();
  assert.equal(card.open, false);
  assert.ok(find(summary, node => node.textContent.includes('反馈可能过期')));
  assert.deepEqual(compactValues(summary), ['50.00 kW', '0.00 kvar', '1.000']);
  panel.update(state(20));
  assert.equal(modelSummary(card), summary); assert.equal(card.open, false);
  assert.ok(find(summary, node => node.textContent === '并网'));
  assert.deepEqual(compactValues(summary), ['20.00 kW', '0.00 kvar', '1.000']);
});

test('大容量场景无100kW上限，光照滑块随设备能力与草稿扩展', async () => {
  const calls = [];
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root, { fetch: async (url, options) => { calls.push(JSON.parse(options.body)); return { ok: true, json: async () => ({}) }; } });
  panel.update(state(245, 250, 275));
  const dc = input(root, 'dc_available_kw');
  const slider = find(root, node => node.type === 'range');
  assert.equal(dc.max, undefined);
  assert.equal(dc.value, '250');
  assert.equal(Number(slider.max), 275);
  assert.equal(Number(slider.value), 250);
  dc.value = '400'; dc.oninput();
  assert.equal(Number(slider.max), 400);
  panel.update(state(100, 100, 110));
  assert.equal(dc.value, '400');
  assert.equal(Number(slider.max), 400);
  await find(root, node => node.dataset.form === 'scenario').onsubmit({ preventDefault() {} });
  assert.equal(calls[0].scenario.dc_available_kw, 400);
});

test('反馈刷新保留控制输入DOM与场景草稿，只更新未编辑的场景值', () => {
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root);
  panel.update(state());
  assert.equal(input(root, 'active_gradient').value, '100');
  assert.equal(input(root, 'reactive_gradient').value, '100');
  const percent = input(root, 'percent');
  percent.value = '37.5';
  const dc = input(root, 'dc_available_kw');
  dc.value = '12'; dc.oninput();
  panel.update(state(25, 40));
  assert.equal(input(root, 'percent'), percent);
  assert.equal(percent.value, '37.5');
  assert.equal(dc.value, '12');
  assert.ok(all(root).some(node => node.textContent === '25.00 kW'));
  assert.equal(input(root, 'grid_frequency_hz').value, '50');
});

test('固定有功输入随反馈Pmax刷新范围，保留DOM与草稿并按最新能力提交', async () => {
  const calls = [];
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root, { fetch: async (url, options) => { calls.push(JSON.parse(options.body)); return { ok: true, json: async () => ({ ok: true }) }; } });
  panel.update(state(49, 50, 80));
  const fixed = input(root, 'fixed');
  assert.equal(Number(fixed.max), 80);
  fixed.value = '70';
  panel.update(state(49, 50, 11));
  assert.equal(input(root, 'fixed'), fixed);
  assert.equal(fixed.value, '70');
  assert.equal(Number(fixed.max), 11);
  const write = find(root, node => node['aria-label'] === '写入固定有功（kW）');
  await write.onclick();
  assert.equal(calls.length, 0);
  assert.equal(fixed.value, '70');
  assert.ok(all(root).some(node => node.textContent.includes('超出合法范围')));
  panel.update(state(49, 50, 80));
  await write.onclick();
  assert.equal(calls[0].value, 70000);
});

test('预设向manual写入、场景全量提交，成功后请求刷新反馈', async () => {
  const calls = []; let applied = 0;
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root, { fetch: async (url, options) => { calls.push({ url, body: JSON.parse(options.body) }); return { ok: true, json: async () => ({ ok: true }) }; }, onApplied: () => applied++ });
  panel.update(state());
  await find(root, node => node.textContent === '50% 限功率').onclick();
  assert.equal(calls[0].url, '/api/manual');
  assert.equal(calls[0].body.address, 40125);
  assert.equal(calls[0].body.value, 500);
  const dc = input(root, 'dc_available_kw'); dc.value = '25'; dc.oninput();
  await find(root, node => node.dataset.form === 'scenario').onsubmit({ preventDefault() {} });
  assert.equal(calls[1].url, '/api/scenario');
  assert.deepEqual(calls[1].body.scenario, { dc_available_kw: 25, grid_line_voltage_v: 400, grid_frequency_hz: 50, ambient_temperature_c: 25, efficiency: 0.98, disturbance_enabled: true });
  assert.equal(applied, 2);
});

test('响应错误保留输入并以文本反馈，非法输入不发请求，旧点表隐藏控制卡', async () => {
  let calls = 0;
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root, { fetch: async () => { calls++; return { ok: false, status: 400, json: async () => ({ error: '功率超范围 <x>' }) }; } });
  panel.update(state());
  await find(root, node => node.textContent === '10 kW').onclick();
  assert.ok(all(root).some(node => node.textContent.includes('功率超范围 <x>')));
  const dc = input(root, 'dc_available_kw'); dc.value = ''; dc.oninput();
  await find(root, node => node.dataset.form === 'scenario').onsubmit({ preventDefault() {} });
  assert.equal(calls, 1);
  assert.equal(dc.value, '');
  panel.update({ buses: [{ name: 'old', slaves: [{ device_id: 1 }] }] });
  assert.equal(root.hidden, true);
});

test('状态断线标明反馈过期，配置重建清除运行场景草稿', () => {
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root);
  panel.update(state());
  const dc = input(root, 'dc_available_kw'); dc.value = '12'; dc.oninput();
  panel.setError();
  assert.ok(all(root).some(node => node.textContent.includes('反馈可能过期')));
  panel.reset(); panel.update(state(50, 40));
  assert.notEqual(input(root, 'dc_available_kw'), dc);
  assert.equal(input(root, 'dc_available_kw').value, '40');
});

test('有功趋势由每次服务端state反馈采样，不会重建输入卡', () => {
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root);
  panel.update({ ...state(50), now: '2026-10-11T10:00:00Z' });
  const card = modelCard(root); card.open = true;
  const percent = input(root, 'percent');
  panel.update({ ...state(25), now: '2026-10-11T10:00:02Z' });
  assert.equal(modelCard(root), card); assert.equal(card.open, true);
  assert.equal(input(root, 'percent'), percent);
  assert.equal(find(root, node => node.dataset.series === 'actual').points, '0.0,20.0 320.0,70.0');
  assert.equal(find(root, node => node.dataset.series === 'target').points, '0.0,20.0 320.0,70.0');
  card.open = false;
  panel.update({ ...state(20), now: '2026-10-11T10:00:04Z' });
  assert.equal(modelCard(root), card); assert.equal(card.open, false);
  assert.equal(find(root, node => node.dataset.series === 'actual').points, '0.0,20.0 160.0,70.0 320.0,80.0');
});
