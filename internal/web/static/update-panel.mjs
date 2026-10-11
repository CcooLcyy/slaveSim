import { createUpdateClient, pollDelay } from './update-model.mjs';
import { createUpdateReload } from './update-reload.mjs';

const panel = document.getElementById('update-panel');
if (panel) {
  const el = id => document.getElementById(`update-${id}`);
  let timer;
  const autoRefresh = createUpdateReload({
    fetch: window.fetch.bind(window),
    reload: () => window.location.reload(),
    onReady: () => {
      clearTimeout(timer);
      el('message').textContent = '更新完成，模拟器已恢复，正在刷新网页…';
      console.info('程序更新完成，模拟器接口已恢复，自动刷新网页');
    },
  });
  function render(snapshot) {
    const { view } = snapshot;
    panel.dataset.tone = view.tone;
    panel.setAttribute('aria-busy', String(view.busy));
    el('current').textContent = view.currentVersion;
    el('latest').textContent = view.latestVersion;
    el('channel').textContent = view.channel;
    el('badge').textContent = view.badge;
    el('message').textContent = view.message;
    el('source').textContent = view.source || '未配置';
    if (view.source) el('source').setAttribute('href', view.source);
    else el('source').removeAttribute('href');
    el('check').disabled = view.checkDisabled;
    el('apply').disabled = view.applyDisabled;
    el('retry').hidden = !view.showRetry;
    autoRefresh.observe(snapshot).catch(error => console.error('更新完成后刷新网页失败', error));
  }
  const client = createUpdateClient({ fetch: window.fetch.bind(window), onChange: render });
  function schedule() {
    clearTimeout(timer);
    const snapshot = client.snapshot();
    timer = setTimeout(poll, pollDelay(snapshot.status, snapshot.pending));
  }
  async function poll() { await client.refresh(); schedule(); }
  for (const action of ['check', 'apply']) {
    el(action).addEventListener('click', async () => {
      if (action === 'apply' && !client.snapshot().view.applyDisabled) autoRefresh.begin(client.snapshot());
      await client.action(action); schedule();
    });
  }
  el('retry').addEventListener('click', poll);
  window.addEventListener('online', poll);
  render(client.snapshot());
  poll();
}
