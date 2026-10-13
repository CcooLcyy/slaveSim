import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

// 执行页面真实脚本，并记录布局读取与DOM写入，验证高频日志不会逐条触发布局。
const source = readFileSync(new URL('../internal/web/static/index.html', import.meta.url), 'utf8').match(/<script>\s*([\s\S]*?)<\/script>/)[1];
function fixture() {
  let reads = 0; let writes = 0; let scrollTop = 0; let timerId = 0;
  const timers = new Map(); const listeners = new Map(); const nodes = new Map();
  const makeNode = tag => ({ tag, children: [], append(...items) { this.children.push(...items); } });
  const logs = makeNode('div');
  Object.defineProperties(logs, {
    scrollTop: { get() { reads++; return scrollTop; }, set(value) { scrollTop = value; } },
    clientHeight: { get() { reads++; return 100; } },
    scrollHeight: { get() { reads++; return this.children.length * 20; } },
    childElementCount: { get() { return this.children.length; } },
    firstChild: { get() { return this.children[0]; } },
    innerHTML: { set() { this.children = []; writes++; } },
  });
  logs.appendChild = node => { writes++; logs.children.push(...(node.tag === 'fragment' ? node.children : [node])); };
  logs.removeChild = node => { writes++; logs.children.splice(logs.children.indexOf(node), 1); };
  nodes.set('logs', logs);
  const document = {
    hidden: false,
    createElement: makeNode, createDocumentFragment: () => makeNode('fragment'),
    getElementById(id) { if (!nodes.has(id)) nodes.set(id, {}); return nodes.get(id); },
    addEventListener(type, listener) { listeners.set(type, listener); },
  };
  const window = { addEventListener() {}, dispatchEvent() {} };
  const context = vm.createContext({ document, window, console, Event: class {},
    EventSource: class {}, fetch: () => new Promise(() => {}), setInterval() {},
    setTimeout(callback) { timers.set(++timerId, callback); return timerId; },
    clearTimeout(id) { timers.delete(id); },
  });
  vm.runInContext(source, context);
  return {
    logs, timers, count: () => ({ reads, writes }),
    push(count, start = 0) { for (let index = 0; index < count; index++) context.appendLog({ time: '2026-10-13T00:00:00Z', summary: String(start + index), frame: '<报文>' }); },
    flush() { const pending = [...timers.values()]; timers.clear(); pending.forEach(callback => callback()); },
    clear: () => nodes.get('clear').onclick(),
    pause: () => nodes.get('pause').onclick({ target: nodes.get('pause') }),
    hide(value) { document.hidden = value; listeners.get('visibilitychange')?.(); },
    readHistory(value) { scrollTop = value; },
    top: () => scrollTop,
  };
}
const summary = row => row.children.at(-1).textContent;

test('高频报文先进入有界队列，一次批量渲染只读取常数次布局', () => {
  const page = fixture(); page.push(10000);
  assert.deepEqual(page.count(), { reads: 0, writes: 0 });
  assert.equal(page.timers.size, 1);
  page.flush();
  assert.equal(page.logs.children.length, 2000);
  assert.equal(summary(page.logs.children[0]), '8000');
  assert.equal(summary(page.logs.children.at(-1)), '9999');
  assert.equal(page.count().writes, 1);
  assert.ok(page.count().reads <= 5);
});

test('已满日志长期接收仍保持2000行，追加按批次执行', () => {
  const page = fixture(); page.push(2000); page.flush();
  for (let index = 0; index < 100; index++) { page.push(30, 2000 + index * 30); page.flush(); }
  assert.equal(page.logs.children.length, 2000);
  assert.equal(summary(page.logs.children[0]), '3000');
  assert.equal(summary(page.logs.children.at(-1)), '4999');
  assert.ok(page.count().reads <= 505);
});

test('清空丢弃待渲染报文，延迟任务不会重新显示旧日志', () => {
  const page = fixture(); page.push(10); page.clear(); page.flush();
  assert.equal(page.logs.children.length, 0);
  page.push(1, 20); page.flush();
  assert.equal(summary(page.logs.children[0]), '20');
});

test('暂停保留已显示日志并丢弃待渲染日志，继续只显示新报文', () => {
  const page = fixture(); page.push(1); page.flush(); page.push(10, 10); page.pause();
  page.push(10, 20); page.flush(); assert.equal(page.logs.children.length, 1);
  page.pause(); page.push(1, 30); page.flush();
  assert.deepEqual(page.logs.children.map(summary), ['0', '30']);
});

test('后台不渲染且队列有界，返回前台展示最近日志', () => {
  const page = fixture(); page.push(1); page.hide(true); page.flush(); page.push(10000);
  assert.equal(page.logs.children.length, 0);
  assert.equal(page.timers.size, 0);
  page.hide(false); assert.equal(page.timers.size, 1); page.flush();
  assert.equal(page.logs.children.length, 2000);
  assert.equal(summary(page.logs.children[0]), '8000');
});

test('浏览历史时裁剪旧行保持阅读位置，底部继续跟随且文本不解析为HTML', () => {
  const page = fixture(); page.push(2000); page.flush();
  assert.equal(page.top(), 40000);
  page.readHistory(1000); page.push(10, 2000); page.flush();
  assert.equal(page.top(), 800);
  assert.ok(page.logs.children.at(-1).children.some(value => typeof value === 'string' && value.includes('<报文>')));
});
