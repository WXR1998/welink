/**
 * screenshot.ts — 截取 DOM 元素为图片并复制到剪贴板
 *
 * 实现：clone 元素到离屏 wrapper → 强制 CJK 字体 → html2canvas → 剪贴板
 * 三级回退：navigator.clipboard.write → execCommand('copy') → 下载图片
 */

import html2canvas from 'html2canvas';

const CJK_FONT_STACK =
  "'PingFang SC', 'Hiragino Sans GB', 'Heiti SC', 'Microsoft YaHei', " +
  "'WenQuanYi Micro Hei', 'Noto Sans CJK SC', system-ui, -apple-system, sans-serif";

export interface ScreenshotResult {
  ok: boolean;
  method: 'clipboard' | 'execCommand' | 'download' | 'failed';
  path?: string;
}

/**
 * 将 DOM 元素截图为 PNG blob
 */
export async function captureElementToBlob(element: HTMLElement): Promise<Blob> {
  // Clone the element so we don't modify the original
  const clone = element.cloneNode(true) as HTMLElement;

  // Remove truncate/ellipsis so full text is visible
  clone.querySelectorAll<HTMLElement>('.truncate').forEach(el => {
    el.classList.remove('truncate');
    el.style.whiteSpace = 'normal';
    el.style.textOverflow = 'clip';
    el.style.overflow = 'visible';
  });
  clone.querySelectorAll<HTMLElement>('[style*="text-overflow"]').forEach(el => {
    if (el.style.textOverflow === 'ellipsis') el.style.textOverflow = 'clip';
  });

  // Determine background from the element's computed style
  let bg = '#ffffff';
  try {
    const computed = window.getComputedStyle(element);
    if (computed.backgroundColor && computed.backgroundColor !== 'rgba(0, 0, 0, 0)') {
      bg = computed.backgroundColor;
    }
  } catch { /* use default white */ }

  // Wrap in an off-screen container with proper background
  const wrapper = document.createElement('div');
  wrapper.style.cssText = `
    position: fixed;
    left: -10000px;
    top: 0;
    background: ${bg};
    padding: 16px;
    font-family: ${CJK_FONT_STACK};
    z-index: -1;
  `;
  wrapper.appendChild(clone);
  document.body.appendChild(wrapper);

  try {
    // Wait for fonts and images
    if (document.fonts && document.fonts.ready) {
      try { await document.fonts.ready; } catch { /* ignore */ }
    }
    const imgs = Array.from(wrapper.querySelectorAll('img'));
    await Promise.all(imgs.map(img => {
      if (img.complete && img.naturalWidth > 0) return Promise.resolve();
      return new Promise<void>(resolve => {
        img.addEventListener('load', () => resolve(), { once: true });
        img.addEventListener('error', () => resolve(), { once: true });
      });
    }));
    await new Promise<void>(resolve => {
      requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
    });

    const canvas = await html2canvas(wrapper, {
      scale: 2,
      backgroundColor: bg,
      useCORS: true,
      logging: false,
      onclone: (clonedDoc) => {
        // Force CJK font on all non-mono elements
        const all = clonedDoc.querySelectorAll<HTMLElement>('*');
        all.forEach(el => {
          let cur = '';
          try {
            cur = clonedDoc.defaultView?.getComputedStyle(el).fontFamily ?? '';
          } catch { /* ignore */ }
          if (!/mono|courier|consolas|menlo/i.test(cur)) {
            el.style.fontFamily = CJK_FONT_STACK;
          }
          // Remove ellipsis
          if (el.style.textOverflow === 'ellipsis') el.style.textOverflow = 'clip';
          if (el.classList.contains('truncate')) {
            el.classList.remove('truncate');
            el.style.whiteSpace = 'normal';
            el.style.overflow = 'visible';
          }
        });
      },
    });

    return new Promise<Blob>((resolve, reject) => {
      canvas.toBlob((blob) => {
        if (blob) resolve(blob);
        else reject(new Error('canvas.toBlob returned null'));
      }, 'image/png');
    });
  } finally {
    if (wrapper.parentNode) wrapper.parentNode.removeChild(wrapper);
  }
}

/**
 * 截取 DOM 元素并复制到剪贴板（或下载）
 */
export async function screenshotElement(element: HTMLElement, filename: string = `welink-${Date.now()}.png`): Promise<ScreenshotResult> {
  const blob = await captureElementToBlob(element);

  // 方案 1: navigator.clipboard.write (需要安全上下文 HTTPS/localhost)
  if (typeof navigator !== 'undefined' && navigator.clipboard && typeof ClipboardItem !== 'undefined') {
    try {
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]);
      return { ok: true, method: 'clipboard' };
    } catch {
      // PNG 不被支持或权限被拒，继续尝试 legacy 方案
    }
  }

  // 方案 2: legacy execCommand('copy')
  const ok = await copyImageViaExecCommand(blob);
  if (ok) return { ok: true, method: 'execCommand' };

  // 方案 3: 下载图片
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.download = filename;
  link.href = url;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  return { ok: true, method: 'download', path: filename };
}

/**
 * Legacy fallback: copy image via contenteditable + execCommand('copy').
 */
async function copyImageViaExecCommand(blob: Blob): Promise<boolean> {
  const url = URL.createObjectURL(blob);
  const img = document.createElement('img');
  img.src = url;
  img.style.position = 'fixed';
  img.style.left = '-9999px';
  img.style.top = '0';

  await new Promise<void>((resolve, reject) => {
    img.onload = () => resolve();
    img.onerror = () => reject(new Error('img load fail'));
  });

  document.body.appendChild(img);

  const range = document.createRange();
  range.selectNode(img);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);

  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }

  sel?.removeAllRanges();
  document.body.removeChild(img);
  URL.revokeObjectURL(url);
  return ok;
}
