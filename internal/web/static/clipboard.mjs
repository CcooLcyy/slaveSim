// HTTP 联调页面可能没有 Clipboard API，因此保留用户点击时可用的复制回退。
export async function copyText(text, document = globalThis.document, navigator = globalThis.navigator) {
  if (typeof navigator?.clipboard?.writeText === 'function') {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // 浏览器拒绝 Clipboard API 时继续尝试传统复制方式。
    }
  }

  if (!document?.body || typeof document.execCommand !== 'function') {
    throw new Error('复制失败，请手动选择路径复制。');
  }

  const activeElement = document.activeElement;
  const view = document.defaultView;
  const scrollX = view?.scrollX ?? 0;
  const scrollY = view?.scrollY ?? 0;
  const scrollPositions = [];
  for (let node = activeElement; node; node = node.parentElement) {
    scrollPositions.push([node, node.scrollLeft, node.scrollTop]);
  }
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.readOnly = true;
  textarea.tabIndex = -1;
  textarea.setAttribute('aria-hidden', 'true');
  Object.assign(textarea.style, { position: 'fixed', top: '0', left: '0', opacity: '0', fontSize: '16px' });

  try {
    document.body.appendChild(textarea);
    textarea.focus({ preventScroll: true });
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    if (!document.execCommand('copy')) throw new Error('浏览器未完成复制。');
  } catch (cause) {
    throw new Error('复制失败，请手动选择路径复制。', { cause });
  } finally {
    textarea.remove();
    if (activeElement?.isConnected !== false && typeof activeElement?.focus === 'function') {
      try {
        activeElement.focus({ preventScroll: true });
      } catch {
        try { activeElement.focus(); } catch { /* 已失效的焦点不影响复制结果。 */ }
      }
    }
    for (const [node, left, top] of scrollPositions) {
      node.scrollLeft = left;
      node.scrollTop = top;
    }
    if (typeof view?.scrollTo === 'function') view.scrollTo(scrollX, scrollY);
  }
}
