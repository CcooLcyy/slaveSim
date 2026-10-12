// 在页面绘制前应用主题；浏览器禁止存储时仍允许当前页面切换。
(() => {
  const key = 'slavesim-theme';
  const normalize = value => ['system', 'light', 'dark'].includes(value) ? value : 'dark';
  const media = window.matchMedia?.('(prefers-color-scheme: dark)');
  let preference = 'dark';
  try { preference = normalize(window.localStorage.getItem(key)); } catch {}

  const apply = () => {
    document.documentElement.dataset.theme = preference === 'system'
      ? (media?.matches ? 'dark' : 'light') : preference;
    const select = document.getElementById('theme-select');
    if (select) select.value = preference;
  };
  apply();
  media?.addEventListener('change', () => { if (preference === 'system') apply(); });
  window.addEventListener('storage', event => {
    if (event.key !== key && event.key !== null) return;
    // sessionStorage 的同名键不属于主题偏好。
    try { if (event.storageArea && event.storageArea !== window.localStorage) return; } catch {}
    preference = normalize(event.newValue);
    apply();
  });
  document.addEventListener('DOMContentLoaded', () => {
    const select = document.getElementById('theme-select');
    if (!select) return;
    apply();
    select.addEventListener('change', () => {
      preference = normalize(select.value);
      try { window.localStorage.setItem(key, preference); } catch {}
      apply();
    });
  });
})();
