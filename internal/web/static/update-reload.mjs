const text = value => typeof value === 'string' ? value : '';
const identity = status => ({ job: text(status?.job_id), version: text(status?.current_version) });
const key = status => JSON.stringify(identity(status));

// 页面只监听真实更新任务；服务端持久化的历史成功记录不能形成刷新循环。
export function createUpdateReload({ fetch: fetchImpl, reload, onReady = () => {} }) {
  let armed = null;
  let probing = false;
  let reloaded = false;
  let readyKey = null;

  function begin(snapshot) {
    if (reloaded) return;
    armed = { baseline: identity(snapshot?.status), updatingJob: null };
    readyKey = null;
  }

  async function observe(snapshot) {
    if (reloaded) return false;
    const status = snapshot?.status;
    if (!status || snapshot.pending || snapshot.disconnected || snapshot.unavailable) {
      readyKey = null;
      return false;
    }
    if (status.state === 'updating') {
      if (!armed) begin(snapshot);
      armed.updatingJob = text(status.job_id);
      readyKey = null;
      return false;
    }
    if (status.state !== 'succeeded') {
      armed = null;
      readyKey = null;
      return false;
    }
    if (!armed) return false;
    const current = identity(status);
    const completed = armed.updatingJob !== null && current.job === armed.updatingJob;
    const newJob = current.job && current.job !== armed.baseline.job;
    const newVersion = current.version && current.version !== armed.baseline.version;
    if (!completed && !newJob && !newVersion) {
      readyKey = null;
      return false;
    }
    readyKey = key(status);
    if (probing) return false;
    const task = armed;
    const candidate = readyKey;
    probing = true;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 10000);
    let state;
    try {
      // 管理进程先提交成功，再确认模拟器接口已恢复，避免重启空窗中刷新页面。
      const response = await fetchImpl('/api/state', {
        method: 'GET', cache: 'no-store', credentials: 'same-origin', signal: controller.signal,
      });
      if (!response.ok) return false;
      state = await response.json();
      if (!state || !Array.isArray(state.buses)) return false;
    } catch {
      // 下一次更新状态轮询会重试，断线期间不重发更新操作。
      return false;
    } finally {
      clearTimeout(timer);
      probing = false;
    }
    if (reloaded || armed !== task || readyKey !== candidate) return false;
    reloaded = true;
    try { onReady(state); } finally { reload(); }
    return true;
  }

  return { begin, observe };
}
