import test from 'node:test';
import assert from 'node:assert/strict';
import { createUpdateClient, getUpdateView, pollDelay, publicSource } from '../internal/web/static/update-model.mjs';

const source = 'https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/ci/latest.json';
const status = (extra = {}) => ({ enabled: true, current_version: 'v1', latest_version: 'v2', source_url: source,
  channel: 'ci', state: 'available', message: '发现新版本', update_available: true, job_id: '', ...extra });
const response = (body, code = 200) => ({ ok: code < 400, status: code, json: async () => body });

test('所有忙状态以及缺少可用来源时禁用操作', () => {
  for (const state of ['checking', 'updating']) {
    const view = getUpdateView(status({ state }));
    assert.equal(view.checkDisabled, true);
    assert.equal(view.applyDisabled, true);
    assert.equal(pollDelay(status({ state })), 2000);
  }
  for (const source_url of ['', 'javascript:alert(1)', 'https://updates.example/latest.json', 'http://localhost/config', source + '?secret=token', 'https://token@updates.example/slavesim/ci/latest.json']) {
    assert.equal(publicSource(source_url), '');
    const view = getUpdateView(status({ source_url }));
    assert.equal(view.checkDisabled, true);
    assert.equal(view.applyDisabled, true);
    assert.equal(view.source, '');
  }
  assert.equal(getUpdateView(status({ enabled: false })).applyDisabled, true);
  assert.equal(getUpdateView(status({ update_available: false })).applyDisabled, true);
  assert.equal(pollDelay(status()), 5000);
});

test('管理进程配置的其他 HTTPS 公开清单也支持网页更新', () => {
  const source_url = 'https://updates.example/slavesim/stable/latest.json';
  assert.equal(publicSource(source_url), source_url);
  const view = getUpdateView(status({ source_url, channel: 'stable' }));
  assert.equal(view.applyDisabled, false);
  assert.equal(view.channel, '稳定通道');
  const custom = 'https://updates.example:8443/tools/slavesim/stable/latest.json';
  assert.equal(publicSource(custom), custom);
});

test('加载与恢复正在执行的任务只查询状态，不自动更新', async () => {
  const calls = [];
  const client = createUpdateClient({ fetch: async (url, options) => {
    calls.push({ url, options });
    return response(status({ state: 'updating', job_id: 'existing-job' }));
  }});
  await client.refresh();
  await client.refresh();
  assert.equal(client.snapshot().status.job_id, 'existing-job');
  assert.ok(calls.every(({ url, options }) => url === '/api/update/status' && options.method === 'GET'));
  assert.equal(await client.action('apply'), false);
  assert.equal(calls.length, 2);
});

test('检查和更新 POST 只带固定动作头及空对象，重复点击不产生额外请求', async () => {
  const calls = [];
  let finish;
  const client = createUpdateClient({ fetch: async (url, options) => {
    calls.push({ url, options });
    if (url.endsWith('/status')) return response(status());
    return new Promise(resolve => { finish = resolve; });
  }});
  await client.refresh();
  const pending = client.action('check');
  assert.equal(client.snapshot().view.applyDisabled, true);
  assert.equal(await client.action('check'), false);
  assert.equal(await client.action('apply'), false);
  assert.equal(calls.length, 2);
  finish(response(status(), 202));
  await pending;
  const apply = client.action('apply');
  finish(response(status({ state: 'updating', job_id: 'new-job' }), 202));
  await apply;
  for (const call of calls.slice(1)) {
    assert.equal(call.options.method, 'POST');
    assert.equal(call.options.headers['X-SlaveSim-Action'], 'update');
    assert.deepEqual(JSON.parse(call.options.body), {});
  }
  assert.deepEqual(calls.map(call => call.url), ['/api/update/status', '/api/update/check', '/api/update/apply']);
});

test('更新请求断线后保留任务信息、显示重连，恢复只 GET 不重发 POST', async () => {
  let disconnected = false;
  const calls = [];
  const client = createUpdateClient({ fetch: async (url, options) => {
    calls.push({ url, options });
    if (disconnected) throw new TypeError('network failed');
    return response(status({ job_id: 'last-job' }));
  }});
  await client.refresh();
  disconnected = true;
  await client.action('apply');
  assert.equal(client.snapshot().status.job_id, 'last-job');
  assert.match(client.snapshot().view.message, /重连/);
  assert.equal(client.snapshot().view.applyDisabled, true);
  disconnected = false;
  await client.refresh();
  assert.equal(client.snapshot().disconnected, false);
  assert.equal(calls.filter(call => call.options.method === 'POST').length, 1);
  assert.equal(calls.at(-1).url, '/api/update/status');
});

test('HTTP 错误不泄露原始错误文本，409 状态恢复忙任务', async () => {
  let failure = response({ error: 'secret /private/path <script>alert(1)</script>' }, 403);
  const client = createUpdateClient({ fetch: async url => url.endsWith('/status') ? response(status()) : failure });
  await client.refresh();
  await client.action('apply');
  assert.match(client.snapshot().view.message, /403/);
  assert.doesNotMatch(client.snapshot().view.message, /secret|private|script/);
  failure = response(status({ state: 'updating', job_id: 'other-job' }), 409);
  await client.action('apply');
  assert.equal(client.snapshot().status.job_id, 'other-job');
  assert.equal(client.snapshot().view.applyDisabled, true);
});

test('旧原生进程没有更新接口时呈现不可用，禁止请求操作', async () => {
  const calls = [];
  const client = createUpdateClient({ fetch: async (url) => {
    calls.push(url);
    return { ok: false, status: 404, json: async () => { throw new SyntaxError('not JSON'); } };
  }});
  await client.refresh();
  assert.equal(client.snapshot().unavailable, true);
  assert.match(client.snapshot().view.message, /网页更新不可用/);
  assert.equal(await client.action('apply'), false);
  assert.equal(calls.length, 1);
});

test('非法动作及未连接的客户端不会发送操作请求', async () => {
  let calls = 0;
  const client = createUpdateClient({ fetch: async () => { calls++; return response(status()); } });
  assert.equal(await client.action('apply'), false);
  await client.refresh();
  assert.equal(await client.action('install'), false);
  assert.equal(calls, 1);
});
