import { createUpdateClient, pollDelay } from './update-model.mjs';

const panel = document.getElementById('update-panel');
if (panel) {
  const el = id => document.getElementById(`update-${id}`);
  function render({ view }) {
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
  }
  const client = createUpdateClient({ fetch: window.fetch.bind(window), onChange: render });
  let timer;
  function schedule() {
    clearTimeout(timer);
    const snapshot = client.snapshot();
    timer = setTimeout(poll, pollDelay(snapshot.status, snapshot.pending));
  }
  async function poll() { await client.refresh(); schedule(); }
  for (const action of ['check', 'apply']) {
    el(action).addEventListener('click', async () => { await client.action(action); schedule(); });
  }
  el('retry').addEventListener('click', poll);
  window.addEventListener('online', poll);
  render(client.snapshot());
  poll();
}
