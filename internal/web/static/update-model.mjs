const states = new Set(['idle', 'checking', 'available', 'current', 'updating', 'succeeded', 'error']);
const messages = {
  idle: '点击“检查更新”获取最新版本。', checking: '正在检查最新版本…', available: '有新版本可更新。',
  current: '当前已是最新版本。', updating: '正在更新，服务会短暂重连…',
  succeeded: '更新成功，模拟器已恢复运行。', error: '更新失败，请检查结果后重试。',
};

// 来源由管理进程验证；页面再限制 HTTPS 清单格式，避免认证信息和本机配置路径。
export function publicSource(value) {
  if (typeof value !== 'string') return '';
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && Boolean(url.hostname)
      && !url.username && !url.password && !url.search && !url.hash
      && /\/(ci|stable)\/latest\.json$/.test(url.pathname) ? url.href : '';
  } catch { return ''; }
}

const busy = status => status?.state === 'checking' || status?.state === 'updating';
const text = value => typeof value === 'string' ? value.slice(0, 600) : '';

export function pollDelay(status, pending = false) {
  return busy(status) || pending ? 2000 : 5000;
}

export function getUpdateView(status, flags = {}) {
  const source = publicSource(status?.source_url);
  const isBusy = busy(status) || flags.pending === true;
  const disabled = !status?.enabled || !source || isBusy || flags.disconnected || flags.unavailable;
  let message = flags.pending ? '正在提交请求，请稍候…' : text(status?.message) || messages[status?.state] || '正在读取更新状态…';
  let tone = isBusy ? 'busy' : status?.state === 'error' ? 'error' : status?.update_available ? 'available' : 'neutral';
  if (flags.unavailable) message = '网页更新不可用：当前服务未提供更新接口。';
  else if (flags.disconnected) message = '连接中断，正在重连… 状态恢复后可继续查看结果。';
  else if (flags.actionError) { message = flags.actionError; tone = 'error'; }
  else if (status && !source) message = '未配置可用的公开更新源，请由管理员完成配置。';
  else if (status && !status.enabled) message = text(status.message) || '网页更新未启用。';
  if (flags.disconnected || flags.unavailable) tone = 'neutral';
  return {
    source, message, tone, busy: isBusy,
    currentVersion: text(status?.current_version) || '—', latestVersion: text(status?.latest_version) || '待检查',
    channel: status?.channel === 'ci' ? '测试通道' : status?.channel === 'stable' ? '稳定通道' : '—',
    checkDisabled: Boolean(disabled), applyDisabled: Boolean(disabled || status?.update_available !== true),
    showRetry: Boolean(flags.disconnected || flags.unavailable),
    badge: flags.disconnected ? '重连中' : flags.unavailable ? '不可用' : status && !source ? '未配置'
      : status && !status.enabled ? '未启用' : isBusy ? '处理中'
      : status?.update_available ? '可更新' : status?.state === 'succeeded' ? '已更新' : status?.state === 'error' ? '失败'
      : status?.state === 'current' ? '最新版' : '手动更新',
  };
}

function isStatus(value) {
  return value && typeof value === 'object' && typeof value.enabled === 'boolean' && states.has(value.state);
}

// 重连只查询服务器任务状态；从不重发结果不明的更新请求。
export function createUpdateClient({ fetch: fetchImpl, onChange = () => {}, timeoutMs = 10000 }) {
  let status = null;
  let pending = false;
  let refreshing = false;
  let disconnected = false;
  let unavailable = false;
  let actionError = '';
  const snapshot = () => {
    const flags = { pending, disconnected, unavailable, actionError };
    return { status, ...flags, view: getUpdateView(status, flags) };
  };
  const notify = () => onChange(snapshot());
  async function request(path, method) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const options = { method, cache: 'no-store', credentials: 'same-origin', signal: controller.signal };
      if (method === 'POST') {
        options.headers = { 'Content-Type': 'application/json', 'X-SlaveSim-Action': 'update' };
        options.body = '{}';
      }
      const response = await fetchImpl(path, options);
      const body = await response.json().catch(() => null);
      if (!response.ok) {
        const error = new Error(`更新请求失败（HTTP ${response.status}），请稍后重试。`);
        error.httpStatus = response.status;
        error.updateStatus = isStatus(body) ? body : null;
        throw error;
      }
      if (!isStatus(body)) {
        const error = new Error('更新接口返回了无法识别的状态，请稍后重试。');
        error.invalidStatus = true;
        throw error;
      }
      return body;
    } finally { clearTimeout(timer); }
  }
  function failed(error, isAction) {
    if (error.updateStatus) status = error.updateStatus;
    unavailable = !isAction && (error.httpStatus === 404 || error.httpStatus === 405);
    disconnected = !error.httpStatus && !error.invalidStatus;
    actionError = disconnected || unavailable ? '' : error.message;
  }
  return {
    snapshot,
    async refresh() {
      if (refreshing || pending) return false;
      refreshing = true;
      try {
        status = await request('/api/update/status', 'GET');
        disconnected = unavailable = false;
        actionError = '';
        return true;
      } catch (error) { failed(error, false); return false; }
      finally { refreshing = false; notify(); }
    },
    async action(action) {
      const view = snapshot().view;
      if (refreshing || !['check', 'apply'].includes(action) || (action === 'check' ? view.checkDisabled : view.applyDisabled)) return false;
      pending = true;
      actionError = '';
      notify();
      try {
        status = await request(`/api/update/${action}`, 'POST');
        disconnected = unavailable = false;
        return true;
      } catch (error) { failed(error, true); return false; }
      finally { pending = false; notify(); }
    },
  };
}
