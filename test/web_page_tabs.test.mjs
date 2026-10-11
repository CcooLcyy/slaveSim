import test from 'node:test';
import assert from 'node:assert/strict';
import { createPageTabs } from '../internal/web/static/page-tabs.mjs';

function fixture() {
  const nodes = new Map();
  const document = { getElementById(id) {
    if (!nodes.has(id)) nodes.set(id, {
      hidden: false, attributes: {}, listeners: [],
      setAttribute(key, value) { this.attributes[key] = value; },
      addEventListener(_, listener) { this.listeners.push(listener); },
      click() { this.onclick?.(); for (const listener of this.listeners) listener(); },
    });
    return nodes.get(id);
  } };
  return { document, tabs: createPageTabs(document) };
}

test('默认显示运行工作区，三个页签切换后只有选中面板可见', () => {
  const { document } = fixture();
  for (const selected of ['runtime', 'update', 'config', 'runtime']) {
    document.getElementById(`show-${selected}`).click();
    for (const page of ['runtime', 'config', 'update']) {
      assert.equal(document.getElementById(`${page}-panel`).hidden, page !== selected);
      assert.equal(document.getElementById(`show-${page}`).attributes['aria-pressed'], String(page === selected));
    }
  }
});

test('页签切换保留草稿节点，并与配置首次读取的点击处理并存', () => {
  const { document, tabs } = fixture();
  const panel = document.getElementById('config-panel'); panel.draft = { name: '尚未保存' };
  let loaded = 0; document.getElementById('show-config').onclick = () => loaded++;
  document.getElementById('show-config').click();
  tabs.select('update'); tabs.select('config');
  assert.equal(document.getElementById('config-panel'), panel);
  assert.equal(panel.draft.name, '尚未保存'); assert.equal(loaded, 1);
  tabs.select('unknown'); assert.equal(panel.hidden, false);
});
