/**
 * screenshot.ts — 截取 DOM 元素为图片并复制到剪贴板
 *
 * 使用 html2canvas 捕获真实 DOM，支持 Markdown 渲染后的内容。
 * 三级回退：navigator.clipboard.write → execCommand('copy') → 下载图片
 */

import html2canvas from 'html2canvas';

export interface ScreenshotResult {
  ok: boolean;
  method: 'clipboard' | 'execCommand' | 'download' | 'failed';
  path?: string;
}

/**
 * 将 DOM 元素截图为 PNG blob
 */
export async function captureElementToBlob(element: HTMLElement): Promise<Blob> {
  const canvas = await html2canvas(element, {
    scale: 2,
    backgroundColor: null,
    useCORS: true,
    logging: false,
  });
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => {
      if (blob) resolve(blob);
      else reject(new Error('canvas.toBlob returned null'));
    }, 'image/png');
  });
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
