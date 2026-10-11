import test from 'node:test';
import assert from 'node:assert/strict';
import { createUpdateReload } from '../internal/web/static/update-reload.mjs';
import { createUpdateClient } from '../internal/web/static/update-model.mjs';

const snapshot = (state, extra = {}, flags = {}) => ({
  status: { enabled: true, state, job_id: 'job-old', current_version: 'v1', ...extra },
  pending: false, disconnected: false, unavailable: false, ...flags,
});
const success = (extra = {}, flags = {}) => snapshot('succeeded', { job_id: 'job-new', current_version: 'v2', ...extra }, flags);
const response = (body = { buses: [] }, ok = true) => ({ ok, json: async () => body });
function harness(fetchImpl = async () => response()) {
  const calls = [];
  const ready = [];
  let reloads = 0;
  const controller = createUpdateReload({
    fetch: async (...args) => { calls.push(args); return fetchImpl(...args); },
    reload: () => { reloads++; }, onReady: state => ready.push(state),
  });
  return { controller, calls, ready, reloads: () => reloads };
}

test('首次读取持久化的成功记录不会刷新，刷新后的新控制器也不会循环', async () => {
  for (let page = 0; page < 2; page++) {
    const h = harness();
    await h.controller.observe({ status: null });
    await h.controller.observe(success());
    await h.controller.observe(success());
    assert.equal(h.reloads(), 0);
    assert.equal(h.calls.length, 0);
  }
});

test('本页更新成功先确认模拟器状态恢复，再刷新一次', async () => {
  const h = harness();
  h.controller.begin(snapshot('available'));
  assert.equal(await h.controller.observe(success()), true);
  assert.equal(h.reloads(), 1);
  assert.deepEqual(h.ready, [{ buses: [] }]);
  assert.equal(h.calls[0][0], '/api/state');
  assert.equal(h.calls[0][1].method, 'GET');
  assert.equal(h.calls[0][1].cache, 'no-store');
  assert.equal(h.calls[0][1].credentials, 'same-origin');
  await h.controller.observe(success());
  assert.equal(h.reloads(), 1);
  assert.equal(h.calls.length, 1);
});

test('本页提交断线后轮询直接取得新任务成功仍能刷新', async () => {
  const h = harness();
  h.controller.begin(snapshot('available'));
  await h.controller.observe(snapshot('available', {}, { disconnected: true }));
  await h.controller.observe(success());
  assert.equal(h.reloads(), 1);
});

test('打开页面时接续正在更新的任务，同任务成功触发刷新', async () => {
  const h = harness();
  await h.controller.observe(snapshot('updating'));
  await h.controller.observe(success({ job_id: 'job-old', current_version: 'v1' }));
  assert.equal(h.reloads(), 1);
});

test('旧成功记录不会被提交中的快照误判为更新完成', async () => {
  const h = harness();
  const previous = success({ job_id: 'job-old', current_version: 'v1' });
  h.controller.begin(previous);
  await h.controller.observe(previous);
  await h.controller.observe(success({}, { pending: true }));
  await h.controller.observe(previous);
  assert.equal(h.calls.length, 0);
  assert.equal(h.reloads(), 0);
  await h.controller.observe(success());
  assert.equal(h.reloads(), 1);
});

test('成功状态处于待提交、断线或接口不可用时不会探测与刷新', async () => {
  for (const flag of ['pending', 'disconnected', 'unavailable']) {
    const h = harness();
    h.controller.begin(snapshot('available'));
    await h.controller.observe(success({}, { [flag]: true }));
    assert.equal(h.calls.length, 0);
    await h.controller.observe(success());
    assert.equal(h.reloads(), 1);
  }
});

test('缺少新任务号时需要版本变化或观察过同任务更新', async () => {
  const h = harness();
  h.controller.begin(snapshot('available', { job_id: '' }));
  await h.controller.observe(success({ job_id: '', current_version: 'v1' }));
  assert.equal(h.reloads(), 0);
  await h.controller.observe(success({ job_id: '' }));
  assert.equal(h.reloads(), 1);
});

test('只检查、已是最新版及更新失败均不触发刷新且取消旧任务监听', async () => {
  for (const state of ['checking', 'available', 'current', 'error', 'idle']) {
    const h = harness();
    h.controller.begin(snapshot('available'));
    await h.controller.observe(snapshot(state, { job_id: 'job-new' }));
    await h.controller.observe(success());
    assert.equal(h.reloads(), 0);
    assert.equal(h.calls.length, 0);
  }
});

test('没有提交或正在更新的证据，仅检查后取得成功历史不会刷新', async () => {
  const h = harness();
  await h.controller.observe(snapshot('checking'));
  await h.controller.observe(snapshot('available'));
  await h.controller.observe(success());
  assert.equal(h.reloads(), 0);
});

test('模拟器恢复探测的网络、HTTP、JSON和状态结构失败可在下一次轮询重试', async () => {
  const failures = [
    async () => { throw new TypeError('断线'); },
    async () => response({ buses: [] }, false),
    async () => ({ ok: true, json: async () => { throw new SyntaxError('无效 JSON'); } }),
    ...[null, {}, { buses: {} }, { buses: null }].map(body => async () => response(body)),
  ];
  for (const failure of failures) {
    let attempt = 0;
    const h = harness(async () => ++attempt === 1 ? failure() : response());
    h.controller.begin(snapshot('available'));
    assert.equal(await h.controller.observe(success()), false);
    assert.equal(h.reloads(), 0);
    assert.equal(await h.controller.observe(success()), true);
    assert.equal(h.reloads(), 1);
  }
});

test('并发观察成功状态只发出一次模拟器探测并刷新一次', async () => {
  let finish;
  const h = harness(() => new Promise(resolve => { finish = resolve; }));
  h.controller.begin(snapshot('available'));
  const first = h.controller.observe(success());
  const second = h.controller.observe(success());
  assert.equal(h.calls.length, 1);
  finish(response());
  await Promise.all([first, second]);
  assert.equal(h.reloads(), 1);
});

test('恢复探测期间重新断线或任务失败不会用过期结果刷新', async () => {
  for (const next of [success({}, { disconnected: true }), snapshot('error', { job_id: 'job-new' })]) {
    let finish;
    const h = harness(() => new Promise(resolve => { finish = resolve; }));
    h.controller.begin(snapshot('available'));
    const first = h.controller.observe(success());
    await h.controller.observe(next);
    finish(response());
    await first;
    assert.equal(h.reloads(), 0);
  }
});

test('更新客户端提交断线后恢复轮询成功会刷新，且不重发更新请求', async () => {
  const h = harness();
  const observations = [];
  const requests = [];
  let finished = false;
  const client = createUpdateClient({
    fetch: async (url, options) => {
      requests.push({ url, method: options.method });
      if (options.method === 'POST') {
        finished = true;
        throw new TypeError('连接中断');
      }
      const serverStatus = finished ? success().status : snapshot('available').status;
      return response({ ...serverStatus, update_available: !finished,
        source_url: 'https://updates.example/slavesim/ci/latest.json' });
    },
    onChange: value => observations.push(h.controller.observe(value)),
  });
  await client.refresh();
  h.controller.begin(client.snapshot());
  assert.equal(await client.action('apply'), false);
  assert.equal(h.reloads(), 0);
  await client.refresh();
  await Promise.all(observations);
  assert.equal(h.reloads(), 1);
  assert.deepEqual(requests, [
    { url: '/api/update/status', method: 'GET' },
    { url: '/api/update/apply', method: 'POST' },
    { url: '/api/update/status', method: 'GET' },
  ]);
});
