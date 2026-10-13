import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

// 运行页面真实 inline 脚本，避免测试重写轮询算法而掩盖响应顺序问题。
const html = readFileSync(new URL('../internal/web/static/index.html', import.meta.url), 'utf8');
const source = html.match(/<script>\s*([\s\S]*?)<\/script>/)[1];
const settle = () => new Promise(resolve => setImmediate(resolve));

function fixture() {
  const requests = []; const events = []; const listeners = new Map(); const nodes = new Map();
  const document = { addEventListener() {}, getElementById(id) {
    if (!nodes.has(id)) nodes.set(id, { textContent: '', innerHTML: '' });
    return nodes.get(id);
  } };
  class Event { constructor(type) { this.type = type; } }
  class CustomEvent extends Event { constructor(type, options) { super(type); this.detail = options.detail; } }
  const window = {
    addEventListener(type, listener) {
      if (!listeners.has(type)) listeners.set(type, []);
      listeners.get(type).push(listener);
    },
    dispatchEvent(event) {
      events.push(event);
      for (const listener of listeners.get(event.type) || []) listener(event);
    },
  };
  const context = vm.createContext({
    document, window, Event, CustomEvent,
    EventSource: class {}, setInterval() {}, console: { error() {} },
    fetch: url => new Promise((resolve, reject) => requests.push({
      url, reject, complete: value => resolve({ ok: true, json: async () => value }),
    })),
  });
  vm.runInContext(source, context);
  return {
    context, requests,
    refresh: () => vm.runInContext('refresh()', context),
    applyConfig: () => window.dispatchEvent(new Event('config-applied')),
    snapshots: () => events.filter(event => event.type === 'sim-state').map(event => event.detail),
    failures: () => events.filter(event => event.type === 'sim-state-error'),
    current: () => vm.runInContext('state', context),
  };
}
const snapshot = (name, seconds) => ({ now: `2026-10-12T00:00:0${seconds}Z`, buses: [{ name, slaves: [] }] });

test('并发轮询后发请求先返回，旧响应不覆盖已接受的新状态', async () => {
  const page = fixture();
  const second = page.refresh();
  assert.equal(page.requests.length, 2);
  assert.ok(page.requests.every(request => request.url === '/api/state'));
  const newer = snapshot('新总线', 2);
  page.requests[1].complete(newer); await second;
  page.requests[0].complete(snapshot('旧总线', 0)); await settle();
  assert.deepEqual(page.snapshots(), [newer]);
  assert.equal(page.current().buses[0].name, '新总线');
});

test('配置应用后忽略旧epoch的响应，等待新配置反馈后再展示总线', async () => {
  const page = fixture(); page.applyConfig();
  assert.equal(page.requests.length, 2);
  page.requests[0].complete(snapshot('配置已删除的总线', 0)); await settle();
  assert.deepEqual(page.snapshots(), []);
  assert.equal(page.current(), null);
  const rebuilt = snapshot('新配置总线', 2);
  page.requests[1].complete(rebuilt); await settle();
  assert.deepEqual(page.snapshots(), [rebuilt]);
  assert.equal(page.current().buses[0].name, '新配置总线');
});

test('过期请求失败不把已经恢复连接的新状态标记为中断', async () => {
  const page = fixture(); const second = page.refresh();
  page.requests[1].complete(snapshot('已恢复总线', 2)); await second;
  page.requests[0].reject(new Error('旧连接失败')); await settle();
  assert.equal(page.failures().length, 0);
  assert.equal(page.current().buses[0].name, '已恢复总线');
});
