import test from 'node:test';
import assert from 'node:assert/strict';
import { createHuaweiPanel } from '../internal/web/static/huawei-panel.mjs';

// 使用最小DOM替身验证刷新与操作编排，不依赖浏览器布局实现。
class Node {
  constructor(tag, ownerDocument) { this.tagName = tag; this.ownerDocument = ownerDocument; this.children = []; this.dataset = {}; this.listeners = {}; this.value = ''; this.textContent = ''; }
  append(...nodes) { this.children.push(...nodes); for (const node of nodes) if (typeof node === 'object') node.parent = this; }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  setAttribute(key, value) { this[key] = value; }
  removeAttribute(key) { delete this[key]; }
  addEventListener(event, listener) { this.listeners[event] = listener; }
  remove() { this.parent.children = this.parent.children.filter(child => child !== this); }
}
const document = { createElement: tag => new Node(tag, document), createElementNS: (namespace, tag) => new Node(tag, document) };
const state = (active = 50, dc = 51.02) => ({ buses: [{ name: 'Huawei-V3', slaves: [{ device_id: 1, model: {
  kind: 'huawei_v3', active_kw: active, target_active_kw: active, reactive_kvar: 0, target_reactive_kvar: 0, power_factor: 1,
  day_energy_kwh: 0, total_energy_kwh: 54321, status: '并网', active_command: 40125, reactive_command: 40122,
  scenario: { dc_available_kw: dc, grid_line_voltage_v: 400, grid_frequency_hz: 50, ambient_temperature_c: 25, efficiency: 0.98 },
}, registers: [] }] }] });
const find = (node, predicate) => [node, ...node.children.filter(child => typeof child === 'object').flatMap(child => all(child))].find(predicate);
const all = node => [node, ...node.children.filter(child => typeof child === 'object').flatMap(all)];
const input = (root, name) => find(root, node => node.name === name);

test('反馈刷新保留控制输入DOM与场景草稿，只更新未编辑的场景值', () => {
  const root = new Node('div', document);
  const panel = createHuaweiPanel(root);
  panel.update(state());
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
  assert.deepEqual(calls[1].body.scenario, { dc_available_kw: 25, grid_line_voltage_v: 400, grid_frequency_hz: 50, ambient_temperature_c: 25, efficiency: 0.98 });
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
  const percent = input(root, 'percent');
  panel.update({ ...state(25), now: '2026-10-11T10:00:02Z' });
  assert.equal(input(root, 'percent'), percent);
  assert.equal(find(root, node => node.dataset.series === 'actual').points, '0.0,20.0 320.0,70.0');
  assert.equal(find(root, node => node.dataset.series === 'target').points, '0.0,20.0 320.0,70.0');
});
