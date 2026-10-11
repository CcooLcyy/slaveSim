// 页签只切换可见性，配置草稿与运行设备节点由各自面板保留。
export function createPageTabs(document) {
  const pages = ['runtime', 'config', 'update'];
  function select(selected) {
    if (!pages.includes(selected)) return;
    for (const page of pages) {
      document.getElementById(`${page}-panel`).hidden = page !== selected;
      document.getElementById(`show-${page}`).setAttribute('aria-pressed', String(page === selected));
    }
  }
  for (const page of pages) document.getElementById(`show-${page}`).addEventListener('click', () => select(page));
  select('runtime');
  return { select };
}

if (typeof document !== 'undefined') createPageTabs(document);
