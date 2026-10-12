import test from 'node:test';
import assert from 'node:assert/strict';
import { copyText } from '../internal/web/static/clipboard.mjs';

function fixture({ copied = true, failure } = {}) {
  const nodes = [];
  const calls = [];
  const panel = { scrollLeft: 5, scrollTop: 50 };
  const document = {
    body: {
      appendChild(node) { nodes.push(node); },
    },
    defaultView: {
      scrollX: 10, scrollY: 200,
      scrollTo(x, y) { calls.push(['scroll', x, y]); this.scrollX = x; this.scrollY = y; },
    },
    createElement(tag) {
      assert.equal(tag, 'textarea');
      const node = {
        style: {}, value: '',
        setAttribute(key, value) { this[key] = value; },
        focus(options) { calls.push(['textarea-focus', options]); document.activeElement = this; },
        select() { calls.push(['select', this.value]); },
        setSelectionRange(start, end) { calls.push(['range', start, end]); },
        remove() { nodes.splice(nodes.indexOf(this), 1); calls.push(['remove']); },
      };
      return node;
    },
    execCommand(command) {
      calls.push(['command', command]);
      assert.equal(nodes.length, 1);
      assert.equal(nodes[0].readOnly, true);
      assert.equal(nodes[0].style.position, 'fixed');
      if (failure) throw failure;
      return copied;
    },
  };
  const focused = {
    parentElement: panel, scrollLeft: 3, scrollTop: 30,
    focus(options) {
      calls.push(['restore-focus', options]);
      document.activeElement = this;
      // 即便浏览器恢复焦点时滚动了面板或页面，也应恢复用户原来的位置。
      panel.scrollTop = 999; document.defaultView.scrollY = 999;
    },
  };
  document.activeElement = focused;
  return { document, nodes, calls, focused, panel };
}

test('Clipboard API 成功时直接复制，不创建临时输入框', async () => {
  const { document, calls, nodes } = fixture();
  const values = [];
  await copyText('/dev/pts/12', document, { clipboard: { async writeText(value) { values.push(value); } } });
  assert.deepEqual(values, ['/dev/pts/12']);
  assert.deepEqual(calls, []);
  assert.deepEqual(nodes, []);
});

test('Clipboard API 被拒绝时回退复制，并清理临时输入框', async () => {
  const { document, calls, nodes } = fixture();
  await copyText('/srv/serial-sim/tty5', document, { clipboard: { async writeText() { throw new Error('拒绝访问'); } } });
  assert.ok(calls.some(call => call[0] === 'select' && call[1] === '/srv/serial-sim/tty5'));
  assert.ok(calls.some(call => call[0] === 'command' && call[1] === 'copy'));
  assert.deepEqual(nodes, []);
});

test('HTTP 页面没有 Clipboard API 时仍可复制，并恢复焦点、面板和页面滚动', async () => {
  const { document, calls, nodes, focused, panel } = fixture();
  await copyText('/dev/pts/12', document, {});
  assert.deepEqual(calls.find(call => call[0] === 'range'), ['range', 0, 11]);
  assert.equal(document.activeElement, focused);
  assert.deepEqual(calls.find(call => call[0] === 'restore-focus'), ['restore-focus', { preventScroll: true }]);
  assert.equal(focused.scrollLeft, 3); assert.equal(focused.scrollTop, 30);
  assert.equal(panel.scrollLeft, 5); assert.equal(panel.scrollTop, 50);
  assert.equal(document.defaultView.scrollX, 10); assert.equal(document.defaultView.scrollY, 200);
  assert.deepEqual(nodes, []);
});

for (const scenario of [{ copied: false }, { failure: new Error('浏览器拒绝复制') }]) {
  test(`回退${scenario.failure ? '抛异常' : '返回失败'}时抛出中文错误，仍清理输入框并恢复焦点`, async () => {
    const { document, nodes, focused, panel } = fixture(scenario);
    await assert.rejects(copyText('/dev/pts/12', document, {}), /复制失败，请手动选择路径复制/);
    assert.deepEqual(nodes, []);
    assert.equal(document.activeElement, focused);
    assert.equal(panel.scrollTop, 50);
    assert.equal(document.defaultView.scrollY, 200);
  });
}

test('浏览器没有任何复制方式时返回中文错误', async () => {
  await assert.rejects(copyText('/dev/pts/12', {}, {}), /复制失败，请手动选择路径复制/);
});
