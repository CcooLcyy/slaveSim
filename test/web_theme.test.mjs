import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const source = readFileSync(new URL('../internal/web/static/theme.js', import.meta.url), 'utf8');
const storageKey = 'slavesim-theme';

function fixture(options = {}) {
  const stored = new Map(options.stored === undefined ? [] : [[storageKey, options.stored]]);
  const writes = [];
  const listeners = new Map();
  const on = (target, type, listener) => {
    const key = `${target}:${type}`;
    if (!listeners.has(key)) listeners.set(key, []);
    listeners.get(key).push(listener);
  };
  const emit = (target, type, event = {}) => {
    for (const listener of listeners.get(`${target}:${type}`) || []) listener(event);
  };
  const select = {
    value: '',
    addEventListener(type, listener) { on('select', type, listener); },
  };
  const document = {
    readyState: 'loading',
    documentElement: { dataset: {} },
    getElementById(id) { return document.readyState === 'loading' || id !== 'theme-select' ? null : select; },
    addEventListener(type, listener) { on('document', type, listener); },
  };
  const media = {
    matches: Boolean(options.dark),
    addEventListener(type, listener) { on('media', type, listener); },
    addListener(listener) { on('media', 'change', listener); },
  };
  const localStorage = {
    getItem(key) {
      if (options.readError) throw new Error('读取被禁用');
      return stored.get(key) ?? null;
    },
    setItem(key, value) {
      if (options.writeError) throw new Error('保存被禁用');
      stored.set(key, value); writes.push([key, value]);
    },
  };
  const window = {
    document,
    addEventListener(type, listener) { on('window', type, listener); },
    ...(options.noMatchMedia ? {} : { matchMedia(query) {
      assert.equal(query, '(prefers-color-scheme: dark)');
      return media;
    } }),
  };
  Object.defineProperty(window, 'localStorage', { get() {
    if (options.storageError) throw new Error('存储被禁用');
    return localStorage;
  } });
  const context = { window, document, console, addEventListener: window.addEventListener };
  if (window.matchMedia) context.matchMedia = window.matchMedia;
  Object.defineProperty(context, 'localStorage', { get() { return window.localStorage; } });
  runInNewContext(source, context);

  return {
    document, select, stored, writes,
    theme: () => document.documentElement.dataset.theme,
    ready() { document.readyState = 'interactive'; emit('document', 'DOMContentLoaded'); },
    choose(value) { select.value = value; emit('select', 'change', { target: select }); },
    system(dark) { media.matches = dark; emit('media', 'change', { matches: dark }); },
    storage(key, value) {
      if (key === null) stored.clear();
      else if (value === null) stored.delete(key);
      else stored.set(key, value);
      emit('window', 'storage', { key, newValue: value, storageArea: localStorage });
    },
  };
}

test('页面 DOM 尚未就绪时默认深色，系统明暗变化不影响默认选择', () => {
  for (const dark of [false, true]) {
    const h = fixture({ dark });
    assert.equal(h.document.readyState, 'loading');
    assert.equal(h.theme(), 'dark');
    h.ready();
    assert.equal(h.select.value, 'dark');
    h.system(!dark);
    assert.equal(h.theme(), 'dark');
    assert.deepEqual(h.writes, []);
  }
});

test('读取持久化的明暗选择，非法值回退为深色', () => {
  for (const [stored, expected, selected] of [
    ['light', 'light', 'light'], ['dark', 'dark', 'dark'],
    ['system', 'light', 'system'], ['unknown', 'dark', 'dark'], ['', 'dark', 'dark'],
  ]) {
    const h = fixture({ stored, dark: false });
    assert.equal(h.theme(), expected);
    h.ready();
    assert.equal(h.select.value, selected);
  }
});

test('显式保存跟随系统时在 DOM 就绪前应用系统主题并响应变化', () => {
  for (const dark of [false, true]) {
    const h = fixture({ stored: 'system', dark });
    assert.equal(h.theme(), dark ? 'dark' : 'light');
    h.ready(); assert.equal(h.select.value, 'system');
    h.system(!dark);
    assert.equal(h.theme(), dark ? 'light' : 'dark');
  }
});

test('用户切换保存选择并立即应用，系统变化仅影响跟随系统', () => {
  const h = fixture(); h.ready();
  h.choose('dark');
  assert.equal(h.theme(), 'dark');
  h.system(false);
  assert.equal(h.theme(), 'dark');
  h.choose('light'); h.system(true);
  assert.equal(h.theme(), 'light');
  h.choose('system');
  assert.equal(h.theme(), 'dark');
  h.system(false);
  assert.equal(h.theme(), 'light');
  assert.deepEqual(h.writes, [[storageKey, 'dark'], [storageKey, 'light'], [storageKey, 'system']]);
});

test('其他页面修改主题或清空存储时同步主题和选择器', () => {
  const h = fixture({ stored: 'light', dark: false }); h.ready();
  h.storage(storageKey, 'dark');
  assert.equal(h.theme(), 'dark'); assert.equal(h.select.value, 'dark');
  h.storage(storageKey, 'light');
  assert.equal(h.theme(), 'light'); assert.equal(h.select.value, 'light');
  h.storage('unrelated', 'dark');
  assert.equal(h.theme(), 'light'); assert.equal(h.select.value, 'light');
  h.storage(storageKey, 'invalid');
  assert.equal(h.theme(), 'dark'); assert.equal(h.select.value, 'dark');
  h.storage(storageKey, 'light'); h.storage(storageKey, null);
  assert.equal(h.theme(), 'dark'); assert.equal(h.select.value, 'dark');
  h.storage(storageKey, 'light'); h.storage(null, null);
  assert.equal(h.theme(), 'dark'); assert.equal(h.select.value, 'dark');
  h.system(true); h.system(false);
  assert.equal(h.theme(), 'dark');
  assert.deepEqual(h.writes, []);
});

test('DOM 就绪前收到跨页主题变化仍能应用并初始化正确选择', () => {
  const h = fixture();
  h.storage(storageKey, 'dark');
  assert.equal(h.theme(), 'dark');
  h.ready(); assert.equal(h.select.value, 'dark');
});

test('存储 getter 或读取失败时默认深色并允许本页手动切换', () => {
  for (const failure of ['storageError', 'readError']) {
    const h = fixture({ [failure]: true, dark: false });
    assert.equal(h.theme(), 'dark');
    h.ready(); assert.equal(h.select.value, 'dark');
    h.system(true); h.system(false); assert.equal(h.theme(), 'dark');
    h.choose('light'); assert.equal(h.theme(), 'light');
    h.system(true); assert.equal(h.theme(), 'light');
  }
});

test('保存失败不会阻止主题切换或重新跟随系统', () => {
  const h = fixture({ writeError: true }); h.ready();
  h.choose('dark'); assert.equal(h.theme(), 'dark');
  h.choose('system'); assert.equal(h.theme(), 'light');
  h.system(true); assert.equal(h.theme(), 'dark');
});

test('没有 matchMedia 时默认深色，显式选择跟随系统回退浅色', () => {
  const h = fixture({ noMatchMedia: true });
  assert.equal(h.theme(), 'dark');
  h.ready(); assert.equal(h.select.value, 'dark');
  h.choose('dark'); assert.equal(h.theme(), 'dark');
  h.choose('system'); assert.equal(h.theme(), 'light');
  const persisted = fixture({ noMatchMedia: true, stored: 'system' });
  assert.equal(persisted.theme(), 'light');
  persisted.ready(); assert.equal(persisted.select.value, 'system');
});
