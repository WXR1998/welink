/**
 * shareImage.ts — HTML→PNG 实现的分享卡片截图
 * 使用 html-to-image 捕获真实 DOM，marked 渲染 Markdown（含表格）
 * 支持所有现代浏览器 + macOS App WebView
 */

import html2canvas from 'html2canvas';
import { marked } from 'marked';
import QRCode from 'qrcode';

// ─── 类型 ──────────────────────────────────────────────────────────────────────


// ─── 头像预加载（fetch → data URL，避免 html2canvas 截图时异步加载失败）────────

async function fetchAvatarDataUrl(avatarUrl?: string): Promise<string | null> {
  if (!avatarUrl) return null;
  try {
    // no-store 防止浏览器缓存旧联系人头像
    const proxied = `/api/avatar?url=${encodeURIComponent(avatarUrl)}&_t=${Date.now()}`;
    const res = await fetch(proxied, { cache: 'no-store' });
    if (!res.ok) return null;
    const blob = await res.blob();
    return await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.onloadend = () => resolve(reader.result as string);
      reader.onerror = () => reject(new Error('FileReader failed'));
      reader.readAsDataURL(blob);
    });
  } catch {
    return null; // 获取失败时静默降级，显示文字首字母
  }
}

// ─── WebView 检测 & 下载 ───────────────────────────────────────────────────────

function isWebView(): boolean {
  const ua = navigator.userAgent;
  return ua.includes('AppleWebKit') && !ua.includes('Safari') && !ua.includes('Chrome') && !ua.includes('Firefox');
}

/** 下载图片；App 模式返回保存路径，浏览器模式返回文件名 */
async function downloadPng(dataUrl: string, filename: string): Promise<string> {
  if (isWebView()) {
    // App 模式：通过后端写入用户配置的下载目录（默认 ~/Downloads）
    const base64 = dataUrl.split(',')[1] ?? dataUrl;
    const res = await fetch('/api/app/save-file', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ filename, content: base64, encoding: 'base64' }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({})) as { error?: string };
      throw new Error(err.error ?? '保存失败');
    }
    const data = await res.json() as { path?: string };
    return data.path ?? filename;
  } else {
    const link = document.createElement('a');
    link.download = filename;
    link.href = dataUrl;
    link.click();
    return filename;
  }
}

// ─── Markdown 内联样式 ─────────────────────────────────────────────────────────

const MARKDOWN_CSS = `
.sa h1,.sa h2,.sa h3,.sa h4 { font-weight:700; margin:10px 0 5px; color:#1d1d1f; line-height:1.4; }
.sa h1 { font-size:17px; } .sa h2 { font-size:16px; } .sa h3,.sa h4 { font-size:14px; }
.sa p  { margin:5px 0; line-height:1.7; }
.sa ul,.sa ol { padding-left:22px; margin:5px 0; }
.sa li { margin:3px 0; line-height:1.6; }
.sa strong { font-weight:700; }
.sa em { font-style:italic; }
.sa hr { border:none; border-top:1px solid #e5e7eb; margin:10px 0; }
/* inline code：
 * - display:inline-block + white-space:nowrap 保证跨行时不把背景 rect 拆成
 *   html2canvas 无法正确拼接的两段（老 bug：盒子会飘出、遮挡后续几行）
 * - vertical-align:baseline 让它对齐正文基线，不把行高抬起来
 * - word-break:keep-all 再保险一层，长 id 溢出就换行而非切字 */
.sa code { background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:12px; font-family:monospace;
           display:inline-block; white-space:nowrap; vertical-align:baseline; word-break:keep-all; margin:0 2px; }
.sa pre  { background:#f3f4f6; padding:10px 12px; border-radius:8px; margin:8px 0; overflow-x:auto; }
.sa pre code { background:none; padding:0; font-size:12px; display:inline; white-space:pre; word-break:normal; margin:0; }
.sa blockquote { border-left:3px solid #07c160; padding-left:12px; color:#666; margin:8px 0; }
.sa table { border-collapse:collapse; width:100%; margin:10px 0; font-size:13px; }
.sa th { background:#f3f4f6; font-weight:600; padding:8px 10px; border:1px solid #e5e7eb; text-align:left; }
.sa td { padding:7px 10px; border:1px solid #e5e7eb; line-height:1.5; }
.sa tr:nth-child(even) td { background:#fafafa; }
.sa a  { color:#07c160; text-decoration:none; }
`;

// ─── SVG 字符串加载为 HTMLImageElement ────────────────────────────────────────

function loadSvgAsImage(svgStr: string, size: number): Promise<HTMLImageElement | null> {
  return new Promise((resolve) => {
    // 注入明确尺寸，保证 Image 有 naturalWidth/naturalHeight
    const sized = svgStr.replace('<svg ', `<svg width="${size * 2}" height="${size * 2}" `);
    const blob = new Blob([sized], { type: 'image/svg+xml;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const img = new Image();
    const timer = setTimeout(() => { URL.revokeObjectURL(url); resolve(null); }, 4000);
    img.onload = () => { clearTimeout(timer); URL.revokeObjectURL(url); resolve(img); };
    img.onerror = () => { clearTimeout(timer); URL.revokeObjectURL(url); resolve(null); };
    img.src = url;
  });
}

function loadDataUrlAsImage(dataUrl: string): Promise<HTMLImageElement | null> {
  return new Promise((resolve) => {
    const img = new Image();
    const timer = setTimeout(() => resolve(null), 4000);
    img.onload = () => { clearTimeout(timer); resolve(img); };
    img.onerror = () => { clearTimeout(timer); resolve(null); };
    img.src = dataUrl;
  });
}

// ─── GitHub SVG 源码 ───────────────────────────────────────────────────────────

const GITHUB_SVG_SOURCE = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#888" d="M12 0C5.374 0 0 5.373 0 12c0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576C20.566 21.797 24 17.3 24 12c0-6.627-5.373-12-12-12z"/></svg>`;

// ─── 社交体检报告分享图 ─────────────────────────────────────────────────────

export interface ReportImageOptions {
  score: number;
  scoreLabel: string;
  stats: { label: string; value: string }[];
  topContactName?: string;
  topContactAvatar?: string;
  topContactMessages?: number;
  highlights: string[];
}

/** 生成社交体检报告分享图；全部 Canvas 2D 绘制，与 AI 分析共享 header/footer */
export async function generateReportImage(options: ReportImageOptions): Promise<string> {
  const FONT = "system-ui, -apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif";
  const S = 2;
  const W = 640;
  const FF = FONT;

  const [qrDataUrl, faviconImg, githubImg, avatarDataUrl] = await Promise.all([
    QRCode.toDataURL('https://welink.click', { width: 96, margin: 1, color: { dark: '#1d1d1f', light: '#f8f9fb' } }),
    fetch('/favicon.svg').then(r => r.text()).then(svg => loadSvgAsImage(svg, 42)).catch(() => null),
    loadSvgAsImage(GITHUB_SVG_SOURCE, 12),
    fetchAvatarDataUrl(options.topContactAvatar),
  ]);
  const qrImageEl = await loadDataUrlAsImage(qrDataUrl);
  const avatarImageEl = avatarDataUrl ? await loadDataUrlAsImage(avatarDataUrl) : null;
  const year = new Date().getFullYear();

  // 计算内容高度
  const HEADER_H = 84;
  const FOOTER_H = 68;
  const PAD = 36;
  let bodyH = 0;
  bodyH += 60;  // score
  bodyH += 30;  // score label
  bodyH += 30;  // gap
  bodyH += 70;  // stat pills
  bodyH += 24;  // gap
  if (options.topContactName) bodyH += 60; // top contact
  bodyH += 16; // gap
  bodyH += options.highlights.length * 28 + 10; // highlights
  bodyH += 20; // bottom pad

  const totalH = HEADER_H + bodyH + FOOTER_H;
  const cvs = document.createElement('canvas');
  cvs.width = W * S;
  cvs.height = totalH * S;
  const ctx = cvs.getContext('2d')!;

  // ── White background ──
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, W * S, totalH * S);

  // ── Header (same as AI share) ──
  const hGrad = ctx.createLinearGradient(0, 0, W * S, 0);
  hGrad.addColorStop(0, '#09d46a');
  hGrad.addColorStop(1, '#06a850');
  ctx.fillStyle = hGrad;
  ctx.fillRect(0, 0, W * S, HEADER_H * S);

  ctx.fillStyle = 'rgba(255,255,255,0.08)';
  ctx.beginPath();
  ctx.arc((W - 15 + 55) * S, (6 + 55) * S, 55 * S, 0, Math.PI * 2);
  ctx.fill();
  ctx.fillStyle = 'rgba(255,255,255,0.06)';
  ctx.beginPath();
  ctx.arc((W + 25 + 35) * S, (42 + 35) * S, 35 * S, 0, Math.PI * 2);
  ctx.fill();

  if (faviconImg) {
    ctx.save();
    ctx.beginPath();
    const [lx, ly, lw, lh, lr] = [36*S, 21*S, 42*S, 42*S, 10*S];
    ctx.moveTo(lx+lr, ly);
    ctx.lineTo(lx+lw-lr, ly); ctx.arcTo(lx+lw, ly, lx+lw, ly+lr, lr);
    ctx.lineTo(lx+lw, ly+lh-lr); ctx.arcTo(lx+lw, ly+lh, lx+lw-lr, ly+lh, lr);
    ctx.lineTo(lx+lr, ly+lh); ctx.arcTo(lx, ly+lh, lx, ly+lh-lr, lr);
    ctx.lineTo(lx, ly+lr); ctx.arcTo(lx, ly, lx+lr, ly, lr);
    ctx.closePath(); ctx.clip();
    ctx.drawImage(faviconImg, lx, ly, lw, lh);
    ctx.restore();
  }

  const TX = (faviconImg ? 90 : 36) * S;
  ctx.textBaseline = 'middle';
  ctx.fillStyle = '#ffffff';
  ctx.font = `900 ${20*S}px ${FF}`;
  ctx.fillText('WeLink', TX, 32 * S);
  ctx.fillStyle = 'rgba(255,255,255,0.78)';
  ctx.font = `${12*S}px ${FF}`;
  ctx.fillText('微信聊天记录 AI 助手', TX, 55 * S);

  // ── Body ──
  let y = HEADER_H + 40; // start y

  // Score
  ctx.textAlign = 'center';
  const scoreColor = options.score >= 70 ? '#22c55e' : options.score >= 40 ? '#eab308' : '#f87171';
  ctx.fillStyle = scoreColor;
  ctx.font = `900 ${52*S}px ${FF}`;
  ctx.fillText(String(options.score), (W / 2) * S, y * S);
  y += 50;

  // Score label
  ctx.fillStyle = '#999999';
  ctx.font = `${13*S}px ${FF}`;
  ctx.fillText(`社交健康指数 · ${options.scoreLabel}`, (W / 2) * S, y * S);
  y += 36;
  ctx.textAlign = 'left';

  // Stat pills (4 columns)
  const pillW = (W - PAD * 2 - 12 * 3) / 4;
  const pillH = 60;
  for (let i = 0; i < options.stats.length && i < 4; i++) {
    const px = PAD + i * (pillW + 12);
    // pill background
    ctx.fillStyle = '#f8f9fb';
    const roundRect = (x: number, yy: number, w: number, h: number, r: number) => {
      ctx.beginPath();
      ctx.moveTo(x+r, yy); ctx.lineTo(x+w-r, yy); ctx.arcTo(x+w, yy, x+w, yy+r, r);
      ctx.lineTo(x+w, yy+h-r); ctx.arcTo(x+w, yy+h, x+w-r, yy+h, r);
      ctx.lineTo(x+r, yy+h); ctx.arcTo(x, yy+h, x, yy+h-r, r);
      ctx.lineTo(x, yy+r); ctx.arcTo(x, yy, x+r, yy, r);
      ctx.closePath();
    };
    roundRect(px*S, y*S, pillW*S, pillH*S, 10*S);
    ctx.fill();

    // value
    ctx.textAlign = 'center';
    ctx.fillStyle = '#1d1d1f';
    ctx.font = `700 ${16*S}px ${FF}`;
    ctx.fillText(options.stats[i].value, (px + pillW/2)*S, (y + 24)*S);

    // label
    ctx.fillStyle = '#999999';
    ctx.font = `${10*S}px ${FF}`;
    ctx.fillText(options.stats[i].label, (px + pillW/2)*S, (y + 46)*S);
    ctx.textAlign = 'left';
  }
  y += pillH + 24;

  // Top contact
  if (options.topContactName) {
    // green bg
    const tcX = PAD, tcW = W - PAD * 2, tcH = 52;
    ctx.fillStyle = '#f0fdf4';
    const roundRect = (x: number, yy: number, w: number, h: number, r: number) => {
      ctx.beginPath();
      ctx.moveTo(x+r, yy); ctx.lineTo(x+w-r, yy); ctx.arcTo(x+w, yy, x+w, yy+r, r);
      ctx.lineTo(x+w, yy+h-r); ctx.arcTo(x+w, yy+h, x+w-r, yy+h, r);
      ctx.lineTo(x+r, yy+h); ctx.arcTo(x, yy+h, x, yy+h-r, r);
      ctx.lineTo(x, yy+r); ctx.arcTo(x, yy, x+r, yy, r);
      ctx.closePath();
    };
    roundRect(tcX*S, y*S, tcW*S, tcH*S, 12*S);
    ctx.fill();

    // avatar circle
    const avSize = 32;
    const avX = tcX + 10;
    const avY = y + (tcH - avSize) / 2;
    ctx.save();
    ctx.beginPath();
    ctx.arc((avX + avSize/2)*S, (avY + avSize/2)*S, (avSize/2)*S, 0, Math.PI*2);
    ctx.clip();
    if (avatarImageEl) {
      ctx.drawImage(avatarImageEl, avX*S, avY*S, avSize*S, avSize*S);
    } else {
      ctx.fillStyle = '#07c160';
      ctx.fillRect(avX*S, avY*S, avSize*S, avSize*S);
      ctx.fillStyle = '#ffffff';
      ctx.font = `700 ${14*S}px ${FF}`;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.fillText(options.topContactName.charAt(0), (avX+avSize/2)*S, (avY+avSize/2)*S);
      ctx.textAlign = 'left';
    }
    ctx.restore();

    // name
    const nameX = avX + avSize + 10;
    ctx.fillStyle = '#1d1d1f';
    ctx.font = `600 ${14*S}px ${FF}`;
    ctx.textBaseline = 'middle';
    ctx.fillText(options.topContactName, nameX*S, (y + tcH/2 - 9)*S);

    // subtitle
    ctx.fillStyle = '#999999';
    ctx.font = `${11*S}px ${FF}`;
    ctx.fillText(
      `${options.topContactMessages?.toLocaleString() ?? ''} 条消息 · 最佳拍档`,
      nameX*S, (y + tcH/2 + 10)*S
    );

    y += tcH + 20;
  }

  // Highlights
  for (const text of options.highlights) {
    // green dot
    ctx.fillStyle = '#07c160';
    ctx.beginPath();
    ctx.arc((PAD + 4)*S, (y + 2)*S, 3*S, 0, Math.PI*2);
    ctx.fill();

    // text
    ctx.fillStyle = '#1d1d1f';
    ctx.font = `${13*S}px ${FF}`;
    ctx.textBaseline = 'middle';
    ctx.fillText(text, (PAD + 16)*S, y*S);
    y += 28;
  }

  // ── Footer (same as AI share) ──
  const fY = (totalH - FOOTER_H) * S;
  ctx.fillStyle = '#f8f9fb';
  ctx.fillRect(0, fY, W*S, FOOTER_H*S);
  ctx.fillStyle = '#ececec';
  ctx.fillRect(0, fY, W*S, 1*S);

  ctx.textBaseline = 'middle';

  if (githubImg) {
    ctx.drawImage(githubImg, 36*S, fY + (25-6)*S, 12*S, 12*S);
  }
  ctx.fillStyle = '#888888';
  ctx.font = `${11*S}px ${FF}`;
  ctx.fillText('https://github.com/runzhliu/welink', (githubImg ? 52 : 36)*S, fY + 25*S);

  ctx.fillStyle = '#bbbbbb';
  ctx.font = `${10*S}px ${FF}`;
  ctx.fillText(`© ${year} @runzhliu · AGPL-3.0`, 36*S, fY + 43*S);

  const QR_SIZE = 48, QR_R = 36, QR_TOP = (FOOTER_H-QR_SIZE)/2;
  const QR_X = W - QR_R - QR_SIZE;
  if (qrImageEl) {
    ctx.drawImage(qrImageEl, QR_X*S, fY + QR_TOP*S, QR_SIZE*S, QR_SIZE*S);
  }

  ctx.textAlign = 'right';
  const CX = (QR_X - 10) * S;
  ctx.fillStyle = '#555555';
  ctx.font = `700 ${11*S}px ${FF}`;
  ctx.fillText('你也想分析微信聊天记录？', CX, fY + 26*S);
  ctx.fillStyle = '#07c160';
  ctx.font = `${10*S}px ${FF}`;
  ctx.fillText('扫码免费体验 →', CX, fY + 43*S);
  ctx.textAlign = 'left';

  // ── Export ──
  const dataUrl = cvs.toDataURL('image/png');
  const filename = `welink-social-report-${Date.now()}.png`;
  return downloadPng(dataUrl, filename);
}

// ─── 主函数 ───────────────────────────────────────────────────────────────────

/** 生成并下载分享图片；返回保存路径（App 模式）或文件名（浏览器模式） */

// ─── AI 分身聊天截图 ──────────────────────────────────────────────────────────

export interface CloneChatMessage {
  role: 'user' | 'assistant';
  content: string;
}

export interface CloneChatImageOptions {
  contactName: string;
  avatarUrl?: string;
  messages: CloneChatMessage[];
  provider?: string;
  model?: string;
}

/** 生成仿微信聊天界面截图，header/footer 与 AI 分析分享图一致 */
export async function generateCloneChatImage(options: CloneChatImageOptions): Promise<string> {
  const FONT = "system-ui, -apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif";
  const S = 2;
  const W = 640;
  const FF = FONT;

  const [qrDataUrl, faviconImg, githubImg, avatarDataUrl] = await Promise.all([
    QRCode.toDataURL('https://welink.click', { width: 96, margin: 1, color: { dark: '#1d1d1f', light: '#f8f9fb' } }),
    fetch('/favicon.svg').then(r => r.text()).then(svg => loadSvgAsImage(svg, 42)).catch(() => null),
    loadSvgAsImage(GITHUB_SVG_SOURCE, 12),
    fetchAvatarDataUrl(options.avatarUrl),
  ]);
  const qrImageEl = await loadDataUrlAsImage(qrDataUrl);
  const avatarImageEl = avatarDataUrl ? await loadDataUrlAsImage(avatarDataUrl) : null;
  const year = new Date().getFullYear();

  const HEADER_H = 84;
  const FOOTER_H = 68;
  const PAD = 28;
  const CHAT_TOP_H = 52; // 聊天顶栏（联系人名 + 模型信息）
  const MSG_GAP = 16;
  const AVATAR_SIZE = 36;
  const BUBBLE_PAD = 12;
  const MAX_BUBBLE_W = W - PAD * 2 - AVATAR_SIZE - 20;

  // 预计算每条消息高度
  const tmpCvs = document.createElement('canvas');
  const tmpCtx = tmpCvs.getContext('2d')!;
  tmpCtx.font = `${14 * S}px ${FF}`;

  function measureText(text: string, maxW: number): { lines: string[]; height: number } {
    const words = text.split('');
    const lines: string[] = [];
    let cur = '';
    for (const ch of words) {
      if (ch === '\n') { lines.push(cur); cur = ''; continue; }
      const test = cur + ch;
      if (tmpCtx.measureText(test).width / S > maxW - BUBBLE_PAD * 2) {
        lines.push(cur); cur = ch;
      } else {
        cur = test;
      }
    }
    if (cur) lines.push(cur);
    if (lines.length === 0) lines.push('');
    const lineH = 20;
    return { lines, height: lines.length * lineH + BUBBLE_PAD * 2 };
  }

  const msgLayouts = options.messages.map(m => {
    const { lines, height } = measureText(m.content, MAX_BUBBLE_W);
    return { ...m, lines, height: Math.max(height, AVATAR_SIZE + 4) };
  });

  const chatH = CHAT_TOP_H + msgLayouts.reduce((s, m) => s + m.height + MSG_GAP, 0) + 20;
  const totalH = HEADER_H + chatH + FOOTER_H;

  const cvs = document.createElement('canvas');
  cvs.width = W * S;
  cvs.height = totalH * S;
  const ctx = cvs.getContext('2d')!;

  // 白色背景
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, W * S, totalH * S);

  // ── Header（与 AI 分析一致）──
  const hGrad = ctx.createLinearGradient(0, 0, W * S, 0);
  hGrad.addColorStop(0, '#09d46a'); hGrad.addColorStop(1, '#06a850');
  ctx.fillStyle = hGrad;
  ctx.fillRect(0, 0, W * S, HEADER_H * S);

  ctx.fillStyle = 'rgba(255,255,255,0.08)';
  ctx.beginPath(); ctx.arc((W - 15 + 55) * S, (6 + 55) * S, 55 * S, 0, Math.PI * 2); ctx.fill();
  ctx.fillStyle = 'rgba(255,255,255,0.06)';
  ctx.beginPath(); ctx.arc((W + 25 + 35) * S, (42 + 35) * S, 35 * S, 0, Math.PI * 2); ctx.fill();

  if (faviconImg) {
    ctx.save(); ctx.beginPath();
    const [lx, ly, lw, lh, lr] = [36*S, 21*S, 42*S, 42*S, 10*S];
    ctx.moveTo(lx+lr, ly); ctx.lineTo(lx+lw-lr, ly); ctx.arcTo(lx+lw, ly, lx+lw, ly+lr, lr);
    ctx.lineTo(lx+lw, ly+lh-lr); ctx.arcTo(lx+lw, ly+lh, lx+lw-lr, ly+lh, lr);
    ctx.lineTo(lx+lr, ly+lh); ctx.arcTo(lx, ly+lh, lx, ly+lh-lr, lr);
    ctx.lineTo(lx, ly+lr); ctx.arcTo(lx, ly, lx+lr, ly, lr);
    ctx.closePath(); ctx.clip();
    ctx.drawImage(faviconImg, lx, ly, lw, lh); ctx.restore();
  }

  const TX = (faviconImg ? 90 : 36) * S;
  ctx.textBaseline = 'middle'; ctx.fillStyle = '#ffffff';
  ctx.font = `900 ${20*S}px ${FF}`; ctx.fillText('WeLink', TX, 32 * S);
  ctx.fillStyle = 'rgba(255,255,255,0.78)';
  ctx.font = `${12*S}px ${FF}`; ctx.fillText('微信聊天记录 AI 助手', TX, 55 * S);

  // ── 聊天顶栏 ──
  let y = HEADER_H;
  ctx.fillStyle = '#f8f9fb';
  ctx.fillRect(0, y * S, W * S, CHAT_TOP_H * S);
  ctx.fillStyle = '#e5e7eb';
  ctx.fillRect(0, (y + CHAT_TOP_H - 1) * S, W * S, 1 * S);

  ctx.textBaseline = 'middle'; ctx.textAlign = 'center';
  ctx.fillStyle = '#1d1d1f'; ctx.font = `700 ${15*S}px ${FF}`;
  ctx.fillText(`${options.contactName} 的 AI 分身`, (W / 2) * S, (y + CHAT_TOP_H / 2 - 6) * S);
  if (options.provider) {
    ctx.fillStyle = '#999999'; ctx.font = `${10*S}px ${FF}`;
    ctx.fillText(`${options.provider}${options.model ? ' · ' + options.model : ''}`, (W / 2) * S, (y + CHAT_TOP_H / 2 + 12) * S);
  }
  ctx.textAlign = 'left';

  // ── 聊天气泡 ──
  y += CHAT_TOP_H + 12;

  const roundRect = (cx: CanvasRenderingContext2D, x: number, yy: number, w: number, h: number, r: number) => {
    cx.beginPath();
    cx.moveTo(x+r, yy); cx.lineTo(x+w-r, yy); cx.arcTo(x+w, yy, x+w, yy+r, r);
    cx.lineTo(x+w, yy+h-r); cx.arcTo(x+w, yy+h, x+w-r, yy+h, r);
    cx.lineTo(x+r, yy+h); cx.arcTo(x, yy+h, x, yy+h-r, r);
    cx.lineTo(x, yy+r); cx.arcTo(x, yy, x+r, yy, r);
    cx.closePath();
  };

  for (const msg of msgLayouts) {
    const isUser = msg.role === 'user';
    const bubbleW = Math.min(MAX_BUBBLE_W, Math.max(...msg.lines.map(l => tmpCtx.measureText(l).width / S)) + BUBBLE_PAD * 2 + 4);

    if (isUser) {
      // 右侧：绿色气泡
      const bx = W - PAD - bubbleW;
      ctx.fillStyle = '#07c160';
      roundRect(ctx, bx * S, y * S, bubbleW * S, msg.height * S, 12 * S);
      ctx.fill();

      ctx.fillStyle = '#ffffff'; ctx.font = `${14*S}px ${FF}`; ctx.textBaseline = 'top';
      msg.lines.forEach((line, i) => {
        ctx.fillText(line, (bx + BUBBLE_PAD) * S, (y + BUBBLE_PAD + i * 20) * S);
      });
    } else {
      // 左侧：头像 + 灰色气泡
      const avX = PAD;
      const avY = y;

      // 头像
      ctx.save(); ctx.beginPath();
      ctx.arc((avX + AVATAR_SIZE / 2) * S, (avY + AVATAR_SIZE / 2) * S, (AVATAR_SIZE / 2) * S, 0, Math.PI * 2);
      ctx.clip();
      if (avatarImageEl) {
        ctx.drawImage(avatarImageEl, avX * S, avY * S, AVATAR_SIZE * S, AVATAR_SIZE * S);
      } else {
        ctx.fillStyle = '#07c160'; ctx.fillRect(avX * S, avY * S, AVATAR_SIZE * S, AVATAR_SIZE * S);
        ctx.fillStyle = '#fff'; ctx.font = `700 ${16*S}px ${FF}`;
        ctx.textBaseline = 'middle'; ctx.textAlign = 'center';
        ctx.fillText(options.contactName.charAt(0), (avX + AVATAR_SIZE / 2) * S, (avY + AVATAR_SIZE / 2) * S);
        ctx.textAlign = 'left';
      }
      ctx.restore();

      const bx = PAD + AVATAR_SIZE + 8;
      ctx.fillStyle = '#f0f0f0';
      roundRect(ctx, bx * S, y * S, bubbleW * S, msg.height * S, 12 * S);
      ctx.fill();

      ctx.fillStyle = '#1d1d1f'; ctx.font = `${14*S}px ${FF}`; ctx.textBaseline = 'top';
      msg.lines.forEach((line, i) => {
        ctx.fillText(line, (bx + BUBBLE_PAD) * S, (y + BUBBLE_PAD + i * 20) * S);
      });
    }
    y += msg.height + MSG_GAP;
  }

  // ── Footer（与 AI 分析一致）──
  const fY = (totalH - FOOTER_H) * S;
  ctx.fillStyle = '#f8f9fb'; ctx.fillRect(0, fY, W * S, FOOTER_H * S);
  ctx.fillStyle = '#ececec'; ctx.fillRect(0, fY, W * S, 1 * S);

  ctx.textBaseline = 'middle';
  if (githubImg) ctx.drawImage(githubImg, 36*S, fY + (25-6)*S, 12*S, 12*S);
  ctx.fillStyle = '#888888'; ctx.font = `${11*S}px ${FF}`;
  ctx.fillText('https://github.com/runzhliu/welink', (githubImg ? 52 : 36)*S, fY + 25*S);
  ctx.fillStyle = '#bbbbbb'; ctx.font = `${10*S}px ${FF}`;
  ctx.fillText(`© ${year} @runzhliu · AGPL-3.0`, 36*S, fY + 43*S);

  const QR_SIZE = 48, QR_R = 36, QR_TOP = (FOOTER_H-QR_SIZE)/2, QR_X = W - QR_R - QR_SIZE;
  if (qrImageEl) ctx.drawImage(qrImageEl, QR_X*S, fY + QR_TOP*S, QR_SIZE*S, QR_SIZE*S);
  ctx.textAlign = 'right';
  const CX = (QR_X - 10) * S;
  ctx.fillStyle = '#555555'; ctx.font = `700 ${11*S}px ${FF}`;
  ctx.fillText('你也想分析微信聊天记录？', CX, fY + 26*S);
  ctx.fillStyle = '#07c160'; ctx.font = `${10*S}px ${FF}`;
  ctx.fillText('扫码免费体验 →', CX, fY + 43*S);
  ctx.textAlign = 'left';

  // ── Export ──
  const dataUrl = cvs.toDataURL('image/png');
  const safeName = options.contactName.replace(/[^\u4e00-\u9fa5a-zA-Z0-9]/g, '_').slice(0, 20);
  const filename = `welink-clone-${safeName}-${Date.now()}.png`;
  return downloadPng(dataUrl, filename);
}

// ─── AI 对话截图（Canvas 2D + html2canvas 渲染 Markdown）─────────────────────

export interface ScreenshotSubject {
  name: string;
  avatarUrl?: string;
}

export interface AIScreenshotOptions {
  question?: string;
  answer: string;
  subjects?: ScreenshotSubject[];
  isCrossContact?: boolean;
  stats?: {
    provider?: string;
    model?: string;
    elapsedSecs?: number;
    tokensPerSec?: number;
    charCount?: number;
    timestamp?: number;
  };
}

/** 渲染 Markdown 到 canvas（marked 解析 → html2canvas 捕获 DOM） */
async function renderMarkdownCanvas(
  markdown: string,
  contentWidth: number,
  font: string,
): Promise<HTMLCanvasElement> {
  marked.use({ breaks: true });
  const html = marked.parse(markdown) as string;
  const sanitized = html
    .replace(/<script\b[^<]*(?:(?!<\/script>)<[^<]*)*<\/script>/gi, '')
    .replace(/\s+on\w+\s*=\s*["'][^"']*["']/gi, '');

  const wrap = document.createElement('div');
  wrap.style.cssText = `position:fixed;left:-10000px;top:0;width:${contentWidth}px;`;
  const styleEl = document.createElement('style');
  styleEl.textContent = MARKDOWN_CSS;
  const content = document.createElement('div');
  content.className = 'sa';
  content.style.cssText = `font-size:14px;color:#1d1d1f;line-height:1.7;font-family:${font};`;
  content.innerHTML = sanitized;
  wrap.appendChild(styleEl);
  wrap.appendChild(content);
  document.body.appendChild(wrap);

  try {
    if (document.fonts && document.fonts.ready) {
      try { await document.fonts.ready; } catch { /* ignore */ }
    }
    await new Promise<void>(r => requestAnimationFrame(() => requestAnimationFrame(() => r())));
    return await html2canvas(content, {
      scale: 2,
      backgroundColor: null,
      useCORS: true,
      logging: false,
    });
  } finally {
    document.body.removeChild(wrap);
  }
}

export async function generateAIScreenshot(options: AIScreenshotOptions): Promise<{ ok: boolean; method: string; path?: string }> {
  const FONT = "system-ui, -apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif";
  const S = 2;
  const W = 640;
  const FF = FONT;

  const PAD = 28;
  const MSG_GAP = 16;
  const BUBBLE_PAD = 12;
  const AVATAR_SIZE = 36;
  const MAX_BUBBLE_W = W - PAD * 2 - AVATAR_SIZE - 20;
  const LINE_H = 22;

  // Header 布局常量
  const HEADER_PAD_V = 14;
  const HEADER_PAD_H = 28;
  const CHIP_AVATAR = 22;
  const CHIP_GAP = 5;
  const CHIP_PAD_R = 10;
  const CHIP_HEIGHT = 30;
  const CHIP_MARGIN = 8;
  const FOOTER_H = 48;
  const CONTENT_PAD = 20;

  // ── 预加载 subjects 头像 ──
  const subjectImgs: (HTMLImageElement | null)[] = options.subjects
    ? await Promise.all(options.subjects.map(async s => {
        if (!s.avatarUrl) return null;
        const dataUrl = await fetchAvatarDataUrl(s.avatarUrl);
        return dataUrl ? await loadDataUrlAsImage(dataUrl) : null;
      }))
    : [];

  // ── 渲染 Markdown answer 到 sub-canvas ──
  const answerCanvas = options.answer
    ? await renderMarkdownCanvas(options.answer, MAX_BUBBLE_W - BUBBLE_PAD * 2, FONT)
    : null;
  const answerH = answerCanvas ? answerCanvas.height / S : 0;

  // ── 文本测量工具 ──
  const tmpCvs = document.createElement('canvas');
  const tmpCtx = tmpCvs.getContext('2d')!;

  function wrapText(text: string, maxW: number): { lines: string[]; textH: number } {
    const paragraphs = text.split('\n');
    const lines: string[] = [];
    for (const para of paragraphs) {
      if (para === '') { lines.push(''); continue; }
      let cur = '';
      for (const ch of para) {
        const test = cur + ch;
        if (tmpCtx.measureText(test).width / S > maxW - BUBBLE_PAD * 2) {
          lines.push(cur); cur = ch;
        } else {
          cur = test;
        }
      }
      if (cur) lines.push(cur);
    }
    if (lines.length === 0) lines.push('');
    return { lines, textH: lines.length * LINE_H };
  }

  // ── 计算 question (plain text) 布局 ──
  let questionLayout: { lines: string[]; textH: number; bubbleW: number; bubbleH: number } | null = null;
  if (options.question) {
    tmpCtx.font = `${14 * S}px ${FF}`;
    const { lines, textH } = wrapText(options.question, MAX_BUBBLE_W);
    const maxLineW = Math.max(...lines.map(l => tmpCtx.measureText(l).width / S));
    const bubbleW = Math.min(MAX_BUBBLE_W, maxLineW + BUBBLE_PAD * 2);
    const bubbleH = Math.max(textH + BUBBLE_PAD * 2, AVATAR_SIZE);
    questionLayout = { lines, textH, bubbleW, bubbleH };
  }

  // ── 计算 header 高度和 subjects chip 布局 ──
  const hasHeader = options.isCrossContact || (options.subjects && options.subjects.length > 0);
  let headerH = 0;

  type Chip = { name: string; img: HTMLImageElement | null; w: number };
  const chips: Chip[] = [];
  if (options.subjects) {
    for (let i = 0; i < options.subjects.length; i++) {
      const s = options.subjects[i];
      tmpCtx.font = `${12 * S}px ${FF}`;
      const nameW = tmpCtx.measureText(s.name).width / S;
      const w = CHIP_AVATAR + CHIP_GAP + nameW + CHIP_PAD_R;
      chips.push({ name: s.name, img: subjectImgs[i], w });
    }
  }

  // Arrange chips in rows (wrap)
  const chipRows: Chip[][] = [[]];
  let rowW = 0;
  const availW = W - HEADER_PAD_H * 2;
  for (const chip of chips) {
    if (rowW + chip.w > availW && chipRows[chipRows.length - 1].length > 0) {
      chipRows.push([]);
      rowW = 0;
    }
    chipRows[chipRows.length - 1].push(chip);
    rowW += chip.w + CHIP_MARGIN;
  }

  if (hasHeader) {
    if (options.isCrossContact) {
      headerH = HEADER_PAD_V * 2 + 24;
    } else {
      headerH = HEADER_PAD_V * 2 + chipRows.length * CHIP_HEIGHT + Math.max(0, chipRows.length - 1) * 8;
    }
  }

  // ── 计算总高度 ──
  let contentH = 0;
  if (questionLayout) {
    contentH += questionLayout.bubbleH + MSG_GAP;
  }
  if (answerCanvas) {
    contentH += answerH + BUBBLE_PAD * 2;
  }
  contentH += CONTENT_PAD * 2;

  const totalH = headerH + contentH + FOOTER_H;

  // ── 创建主 canvas ──
  const cvs = document.createElement('canvas');
  cvs.width = W * S;
  cvs.height = totalH * S;
  const ctx = cvs.getContext('2d')!;

  // 白色背景（不透明）
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, W * S, totalH * S);

  // roundRect 辅助函数
  const roundRect = (cx: CanvasRenderingContext2D, x: number, yy: number, w: number, h: number, r: number) => {
    cx.beginPath();
    cx.moveTo(x + r, yy); cx.lineTo(x + w - r, yy); cx.arcTo(x + w, yy, x + w, yy + r, r);
    cx.lineTo(x + w, yy + h - r); cx.arcTo(x + w, yy + h, x + w - r, yy + h, r);
    cx.lineTo(x + r, yy + h); cx.arcTo(x, yy + h, x, yy + h - r, r);
    cx.lineTo(x, yy + r); cx.arcTo(x, yy, x + r, yy, r);
    cx.closePath();
  };

  // ── 绘制 header ──
  let y = 0;
  if (hasHeader) {
    ctx.fillStyle = '#ffffff';
    ctx.fillRect(0, 0, W * S, headerH * S);
    // bottom border
    ctx.fillStyle = '#f0f0f0';
    ctx.fillRect(0, (headerH - 1) * S, W * S, 1 * S);

    if (options.isCrossContact) {
      ctx.fillStyle = '#1d1d1f';
      ctx.font = `700 ${14 * S}px ${FF}`;
      ctx.textBaseline = 'middle';
      ctx.textAlign = 'left';
      ctx.fillText('🔍 跨联系人问答', HEADER_PAD_H * S, (HEADER_PAD_V + 12) * S);
    } else {
      // subject chips
      let chipY = HEADER_PAD_V;
      for (const row of chipRows) {
        let chipX = HEADER_PAD_H;
        for (const chip of row) {
          // avatar circle
          const avX = chipX;
          const avY = chipY + (CHIP_HEIGHT - CHIP_AVATAR) / 2;
          ctx.save();
          ctx.beginPath();
          ctx.arc((avX + CHIP_AVATAR / 2) * S, (avY + CHIP_AVATAR / 2) * S, (CHIP_AVATAR / 2) * S, 0, Math.PI * 2);
          ctx.clip();
          if (chip.img) {
            ctx.drawImage(chip.img, avX * S, avY * S, CHIP_AVATAR * S, CHIP_AVATAR * S);
          } else {
            ctx.fillStyle = '#576b95';
            ctx.fillRect(avX * S, avY * S, CHIP_AVATAR * S, CHIP_AVATAR * S);
          }
          ctx.restore();

          // name
          ctx.fillStyle = '#1d1d1f';
          ctx.font = `${12 * S}px ${FF}`;
          ctx.textBaseline = 'middle';
          ctx.textAlign = 'left';
          ctx.fillText(chip.name, (chipX + CHIP_AVATAR + CHIP_GAP) * S, (chipY + CHIP_HEIGHT / 2) * S);

          chipX += chip.w + CHIP_MARGIN;
        }
        chipY += CHIP_HEIGHT + 8;
      }
    }
    y = headerH;
  }

  // ── 绘制 question bubble (user, plain text) ──
  y += CONTENT_PAD;
  if (questionLayout) {
    const bx = W - PAD - questionLayout.bubbleW;
    ctx.fillStyle = '#07c160';
    roundRect(ctx, bx * S, y * S, questionLayout.bubbleW * S, questionLayout.bubbleH * S, 12 * S);
    ctx.fill();

    // 文字垂直居中 + 2px 下移修复
    const textOffsetY = (questionLayout.bubbleH - questionLayout.textH) / 2 + 4;
    ctx.fillStyle = '#ffffff';
    ctx.font = `${14 * S}px ${FF}`;
    ctx.textBaseline = 'top';
    ctx.textAlign = 'left';
    questionLayout.lines.forEach((line, i) => {
      ctx.fillText(line, (bx + BUBBLE_PAD) * S, (y + textOffsetY + i * LINE_H) * S);
    });

    y += questionLayout.bubbleH + MSG_GAP;
  }

  // ── 绘制 answer bubble (AI, markdown) ──
  if (answerCanvas) {
    const bubbleH = answerH + BUBBLE_PAD * 2;
    const bubbleW = MAX_BUBBLE_W;

    // AI avatar (left)
    const avX = PAD;
    const avY = y;
    ctx.save();
    ctx.beginPath();
    ctx.arc((avX + AVATAR_SIZE / 2) * S, (avY + AVATAR_SIZE / 2) * S, (AVATAR_SIZE / 2) * S, 0, Math.PI * 2);
    ctx.clip();
    ctx.fillStyle = '#576b95';
    ctx.fillRect(avX * S, avY * S, AVATAR_SIZE * S, AVATAR_SIZE * S);
    ctx.fillStyle = '#fff';
    ctx.font = `700 ${13 * S}px ${FF}`;
    ctx.textBaseline = 'middle';
    ctx.textAlign = 'center';
    ctx.fillText('AI', (avX + AVATAR_SIZE / 2) * S, (avY + AVATAR_SIZE / 2) * S);
    ctx.restore();

    // Gray bubble background
    const bx = PAD + AVATAR_SIZE + 8;
    ctx.fillStyle = '#f0f0f0';
    roundRect(ctx, bx * S, y * S, bubbleW * S, bubbleH * S, 12 * S);
    ctx.fill();

    // Draw markdown content canvas on top
    ctx.drawImage(answerCanvas, (bx + BUBBLE_PAD) * S, (y + BUBBLE_PAD - 7) * S);

    y += bubbleH;
  }

  // ── 绘制 footer（模型名称 + 生成时间）──
  const fY = (totalH - FOOTER_H) * S;
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, fY, W * S, FOOTER_H * S);
  ctx.fillStyle = '#f0f0f0';
  ctx.fillRect(0, fY, W * S, 1 * S);

  ctx.textBaseline = 'middle';
  ctx.textAlign = 'left';

  // Model name (left)
  const modelParts: string[] = [];
  if (options.stats?.provider) modelParts.push(options.stats.provider);
  if (options.stats?.model) modelParts.push(options.stats.model);
  if (modelParts.length > 0) {
    ctx.fillStyle = '#888888';
    ctx.font = `${11 * S}px ${FF}`;
    ctx.fillText(modelParts.join(' · '), PAD * S, fY + (FOOTER_H / 2) * S);
  }

  // Generation time (right)
  if (options.stats?.timestamp) {
    const d = new Date(options.stats.timestamp);
    const timeStr = `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${d.getHours()}:${String(d.getMinutes()).padStart(2, '0')}`;
    ctx.textAlign = 'right';
    ctx.fillStyle = '#aaaaaa';
    ctx.font = `${11 * S}px ${FF}`;
    ctx.fillText(timeStr, (W - PAD) * S, fY + (FOOTER_H / 2) * S);
  }

  // ── 导出：优先复制到剪贴板，失败则下载 ──
  const blob = await new Promise<Blob | null>(resolve => cvs.toBlob(b => resolve(b), 'image/png'));
  if (!blob) return { ok: false, method: 'failed' };

  if (typeof navigator !== 'undefined' && navigator.clipboard && typeof ClipboardItem !== 'undefined') {
    try {
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]);
      return { ok: true, method: 'clipboard' };
    } catch {
      // fall through to download
    }
  }

  const dataUrl = cvs.toDataURL('image/png');
  const filename = `welink-ai-${Date.now()}.png`;
  await downloadPng(dataUrl, filename);
  return { ok: true, method: 'download', path: filename };
}
