import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Brain, Pin, PinOff, Pencil, Trash2, Search, Loader2, Check, X as XIcon, Plus, Copy, Camera } from 'lucide-react';
import axios from 'axios';
import type { ContactStats, GroupInfo } from '../../types';
import { avatarSrc } from '../../utils/avatar';
import { JobProgressPanel } from './JobProgressPanel';
import { BatchExtractPanel } from './BatchExtractPanel';
import { RelativeTime } from '../common/RelativeTime';

interface MemFact {
  id: number;
  contact_key: string;
  fact: string;
  source_from: number;
  source_to: number;
  pinned: boolean;
  created_at?: number;
  updated_at?: number;
}

interface ContactStat {
  contact_key: string;
  count: number;
  pinned_count: number;
}

interface Props {
  contacts: ContactStats[];
  groups: GroupInfo[];
}

// Memory 库主页 — 浏览 / 搜索 / 编辑 / 置顶 / 删除 LLM 提炼的记忆事实。
// 置顶事实会在 AI 对话时自动塞进 context（由后端 BuildPinnedMemoryBlock 处理）。
// ── 截图渲染辅助函数 ──────────────────────────────────────────────────────────

function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.crossOrigin = 'anonymous';
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error('img load fail'));
    img.src = src;
  });
}

function colorForName(name: string): string {
  let hash = 0;
  for (let i = 0; i < name.length; i++) {
    hash = name.charCodeAt(i) + ((hash << 5) - hash);
  }
  return `hsl(${Math.abs(hash) % 360}, 60%, 55%)`;
}

function wrapText(ctx: CanvasRenderingContext2D, text: string, maxWidth: number): string[] {
  const lines: string[] = [];
  let current = '';
  for (const ch of text) {
    if (ch === '\n') {
      lines.push(current);
      current = '';
      continue;
    }
    const test = current + ch;
    if (ctx.measureText(test).width > maxWidth && current) {
      lines.push(current);
      current = ch;
    } else {
      current = test;
    }
  }
  if (current) lines.push(current);
  return lines;
}

function parseDateTime(dt: string): number {
  // datetime format: "2021-03-08 23:12:05"
  const d = new Date(dt.replace(/-/g, '/'));
  return isNaN(d.getTime()) ? 0 : d.getTime();
}

function formatTimestamp(dt: string): string {
  // "2021-03-08 23:12:05" → "2021-03-08 23:12"
  return dt.length >= 16 ? dt.slice(0, 16) : dt;
}

// Extract the time range prefix from a fact text.
// Fact format: "[2020-01-01 12:34 ~ 2020-01-01 12:40] fact content"
// Returns "[2020-01-01 12:34 ~ 2020-01-01 12:40] " (including trailing space),
// or empty string if the fact has no time range prefix.
function extractTimeRangePrefix(fact: string): string {
  const bracketEnd = fact.indexOf('] ');
  if (bracketEnd > 0) {
    return fact.substring(0, bracketEnd + 2);
  }
  return '';
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.lineTo(x + w - r, y);
  ctx.quadraticCurveTo(x + w, y, x + w, y + r);
  ctx.lineTo(x + w, y + h - r);
  ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  ctx.lineTo(x + r, y + h);
  ctx.quadraticCurveTo(x, y + h, x, y + h - r);
  ctx.lineTo(x, y + r);
  ctx.quadraticCurveTo(x, y, x + r, y);
  ctx.closePath();
}

/**
 * Render an array of chat messages to a JPG blob.
 * - Avatars: circular, left side
 * - Sender names: small gray text above bubble
 * - Content: white rounded bubble
 * - Timestamps: only shown when gap > 5 min from previous message
 */
// 全局字体加载状态
let _fontLoaded = false;
let _fontLoadPromise: Promise<void> | null = null;

async function ensureFontLoaded(): Promise<void> {
  if (_fontLoaded) return;
  if (_fontLoadPromise) return _fontLoadPromise;
  _fontLoadPromise = (async () => {
    try {
      const font = new FontFace('PingFang', 'url(/PingFang.woff2)');
      await font.load();
      document.fonts.add(font);
    } catch {
      // fallback: use system font
    }
    _fontLoaded = true;
  })();
  return _fontLoadPromise;
}

// ── 人名高亮辅助 ──────────────────────────────────────────────────────────────

interface TextSegment {
  text: string;
  isName: boolean;
}

/** 把名称集合组织成首字符索引，加速查找 */
function buildNameIndex(names: Set<string>): Map<string, string[]> {
  const index = new Map<string, string[]>();
  for (const name of names) {
    if (name.length < 2) continue;
    const first = name[0];
    if (!index.has(first)) index.set(first, []);
    index.get(first)!.push(name);
  }
  for (const list of index.values()) {
    list.sort((a, b) => b.length - a.length);
  }
  return index;
}

/** 将文本拆分为普通段 + 人名段，人名段用青色渲染 */
function highlightNames(text: string, nameIndex: Map<string, string[]>): TextSegment[] {
  if (nameIndex.size === 0) return [{ text, isName: false }];
  const segments: TextSegment[] = [];
  let pos = 0;
  while (pos < text.length) {
    const ch = text[pos];
    const candidates = nameIndex.get(ch);
    let matched: string | null = null;
    if (candidates) {
      for (const name of candidates) {
        if (text.startsWith(name, pos)) {
          matched = name;
          break;
        }
      }
    }
    if (matched) {
      segments.push({ text: matched, isName: true });
      pos += matched.length;
    } else {
      let end = pos + 1;
      while (end < text.length) {
        const c2 = text[end];
        const cands2 = nameIndex.get(c2);
        if (cands2) {
          let hit = false;
          for (const name of cands2) {
            if (text.startsWith(name, end)) { hit = true; break; }
          }
          if (hit) break;
        }
        end++;
      }
      segments.push({ text: text.slice(pos, end), isName: false });
      pos = end;
    }
  }
  return segments;
}

async function renderChatToBlob(
  msgs: { datetime: string; sender: string; content: string }[],
  avatarLookup: (sender: string) => string | undefined,
  title: string,
  header?: { name: string; avatarUrl?: string },
  nameSet?: Set<string>,
): Promise<Blob> {
  await ensureFontLoaded();
  const font = '"PingFang", "PingFang SC", "Microsoft YaHei", sans-serif';
  const canvasW = 500;
  const padX = 16;
  const avatarSize = 32;
  const avatarGap = 8;
  const contentX = padX + avatarSize + avatarGap;
  const maxBubbleW = canvasW - contentX - padX - 40;
  const bubblePadH = 10;
  const bubblePadV = 8;
  const lineH = 20;
  const nameH = 15;
  const avatarTopGap = 8;
  const afterGap = 3;
  const tsGap = 14;
  const timeGapThreshold = 5 * 60 * 1000;
  const titleFont = `bold 14px ${font}`;
  const contentFont = `13px ${font}`;
  const tsFont = `11px ${font}`;
  const HEADER_H = header ? 52 : 0;
  const HEADER_AVATAR = 36;
  const nameIndex = nameSet ? buildNameIndex(nameSet) : new Map<string, string[]>();

  // ── 1. Pre-calc title height ───────────────────────────────────────────
  // Title format: first line = time range (centered), then bullet items (left-aligned)
  const tmpCanvas = document.createElement('canvas');
  const tmpCtx = tmpCanvas.getContext('2d')!;
  tmpCtx.font = titleFont;
  const maxTitleW = canvasW - padX * 2;

  // Split title into time range (first line) and bullet items (rest)
  const titleLines = title.split('\n');
  const titleFirstLine = titleLines[0] || '';
  const titleRestRaw = titleLines.slice(1);
  // Wrap each bullet item individually
  const titleRestLines: string[] = [];
  tmpCtx.font = contentFont;
  for (const rawLine of titleRestRaw) {
    const wrapped = wrapText(tmpCtx, rawLine, maxTitleW - padX);
    titleRestLines.push(...wrapped);
  }

  const titleLineH = 20;
  const titlePadTop = 12;
  const titlePadBot = 12;
  const titleH = titlePadTop + (1 + titleRestLines.length) * titleLineH + titlePadBot;

  // ── 2. Pre-calc message layouts ────────────────────────────────────────
  tmpCtx.font = contentFont;
  type LayoutMsg = {
    showTs: boolean;
    showAvatar: boolean;
    wrappedLines: string[];
    bubbleW: number;
    bubbleH: number;
    msgH: number;
  };
  const layouts: LayoutMsg[] = [];
  let totalH = HEADER_H + padX + titleH;

  for (let i = 0; i < msgs.length; i++) {
    const m = msgs[i];
    const prev = i > 0 ? msgs[i - 1] : null;
    const showTs = !prev || (parseDateTime(m.datetime) - parseDateTime(prev.datetime) > timeGapThreshold);
    const showAvatar = !prev || prev.sender !== m.sender || showTs;
    if (showAvatar && i > 0 && !showTs) totalH += avatarTopGap;
    if (showTs && i > 0) totalH += tsGap;
    if (showTs) totalH += 22;
    tmpCtx.font = contentFont;
    const lines = wrapText(tmpCtx, m.content, maxBubbleW - bubblePadH * 2);
    const textW = Math.max(...lines.map(l => tmpCtx.measureText(l).width));
    const bubbleW = Math.min(maxBubbleW, textW + bubblePadH * 2);
    const bubbleH = lines.length * lineH + bubblePadV * 2;
    const msgH = showAvatar ? Math.max(avatarSize, nameH + bubbleH) : bubbleH;
    layouts.push({ showTs, showAvatar, wrappedLines: lines, bubbleW, bubbleH, msgH });
    totalH += msgH + afterGap;
  }
  totalH += padX;

  // ── 3. Create canvas ───────────────────────────────────────────────────
  const canvas = document.createElement('canvas');
  const dpr = Math.max(2, window.devicePixelRatio || 1);
  canvas.width = canvasW * dpr;
  canvas.height = totalH * dpr;
  canvas.style.width = canvasW + 'px';
  canvas.style.height = totalH + 'px';
  const ctx = canvas.getContext('2d')!;
  ctx.scale(dpr, dpr);

  // Background
  ctx.fillStyle = '#ededed';
  ctx.fillRect(0, 0, canvasW, totalH);

  // ── 3.5. Draw header (group avatar + name) ─────────────────────────────
  let headerAvatarEl: HTMLImageElement | null = null;
  if (header?.avatarUrl) {
    try { headerAvatarEl = await loadImage(header.avatarUrl); } catch { /* ignore */ }
  }
  if (header) {
    ctx.fillStyle = '#ffffff';
    ctx.fillRect(0, 0, canvasW, HEADER_H);

    // Avatar (circular)
    const avX = padX;
    const avY = (HEADER_H - HEADER_AVATAR) / 2;
    ctx.save();
    ctx.beginPath();
    ctx.arc(avX + HEADER_AVATAR / 2, avY + HEADER_AVATAR / 2, HEADER_AVATAR / 2, 0, Math.PI * 2);
    ctx.closePath();
    if (headerAvatarEl) { ctx.clip(); ctx.drawImage(headerAvatarEl, avX, avY, HEADER_AVATAR, HEADER_AVATAR); }
    else {
      ctx.fillStyle = colorForName(header.name); ctx.fill();
      ctx.fillStyle = '#fff';
      ctx.font = `bold 14px ${font}`;
      ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
      ctx.fillText(header.name.charAt(0), avX + HEADER_AVATAR / 2, avY + HEADER_AVATAR / 2);
    }
    ctx.restore();

    // Name
    ctx.fillStyle = '#1a1a1a';
    ctx.font = `bold 15px ${font}`;
    ctx.textAlign = 'left'; ctx.textBaseline = 'middle';
    ctx.fillText(header.name, padX + HEADER_AVATAR + 10, HEADER_H / 2);

    // Separator
    ctx.strokeStyle = '#e5e5e5';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(0, HEADER_H);
    ctx.lineTo(canvasW, HEADER_H);
    ctx.stroke();
  }

  // ── 4. Draw title ──────────────────────────────────────────────────────
  ctx.fillStyle = '#f7f7f7';
  ctx.fillRect(0, HEADER_H, canvasW, titleH);
  ctx.textBaseline = 'top';
  let titleY = HEADER_H + titlePadTop;
  // First line: time range, centered, bold
  ctx.fillStyle = '#1a1a1a';
  ctx.font = titleFont;
  ctx.textAlign = 'center';
  ctx.fillText(titleFirstLine, canvasW / 2, titleY);
  titleY += titleLineH;
  // Rest: bullet items, left-aligned (with name highlighting)
  ctx.font = contentFont;
  ctx.textAlign = 'left';
  for (const line of titleRestLines) {
    const segs = highlightNames(line, nameIndex);
    let segX = padX;
    for (const seg of segs) {
      ctx.fillStyle = seg.isName ? '#0891b2' : '#333';
      ctx.fillText(seg.text, segX, titleY);
      segX += tmpCtx.measureText(seg.text).width;
    }
    titleY += titleLineH;
  }

  // Separator line under title
  ctx.strokeStyle = '#dcdcdc';
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(0, HEADER_H + titleH);
  ctx.lineTo(canvasW, HEADER_H + titleH);
  ctx.stroke();

  // ── 5. Pre-load avatars ────────────────────────────────────────────────
  const senderAvatars = new Map<string, HTMLImageElement | null>();
  const uniqueSenders = [...new Set(msgs.map(m => m.sender))];
  for (const sender of uniqueSenders) {
    const avatarUrl = avatarLookup(sender);
    if (avatarUrl) {
      try { senderAvatars.set(sender, await loadImage(avatarUrl)); }
      catch { senderAvatars.set(sender, null); }
    } else {
      senderAvatars.set(sender, null);
    }
  }

  // ── 6. Draw messages ───────────────────────────────────────────────────
  let y = HEADER_H + padX + titleH;
  ctx.textBaseline = 'top';

  for (let i = 0; i < msgs.length; i++) {
    const m = msgs[i];
    const layout = layouts[i];

    // Extra gap above avatar message (but not when timestamp is shown)
    if (layout.showAvatar && i > 0 && !layout.showTs) y += avatarTopGap;

    // Timestamp separator
    if (layout.showTs) {
      if (i > 0) y += tsGap;
      ctx.fillStyle = '#b2b2b2';
      ctx.font = tsFont;
      ctx.textAlign = 'center';
      ctx.fillText(formatTimestamp(m.datetime), canvasW / 2, y);
      y += 22;
    }

    const avatarX = padX;
    const avatarY = y;

    if (layout.showAvatar) {
      // Draw avatar
      const senderColor = colorForName(m.sender);
      const avatarImg = senderAvatars.get(m.sender);
      ctx.save();
      ctx.beginPath();
      ctx.arc(avatarX + avatarSize / 2, avatarY + avatarSize / 2, avatarSize / 2, 0, Math.PI * 2);
      ctx.closePath();
      if (avatarImg) { ctx.clip(); ctx.drawImage(avatarImg, avatarX, avatarY, avatarSize, avatarSize); }
      else {
        ctx.fillStyle = senderColor; ctx.fill();
        ctx.fillStyle = '#fff';
        ctx.font = `bold 14px ${font}`;
        ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
        ctx.fillText(m.sender.charAt(0), avatarX + avatarSize / 2, avatarY + avatarSize / 2);
      }
      ctx.restore();

      // Sender name
      ctx.fillStyle = '#888';
      ctx.font = `11px ${font}`;
      ctx.textAlign = 'left'; ctx.textBaseline = 'top';
      ctx.fillText(m.sender, contentX, y + 1);

      // Content bubble — vertically centered text
      const bubbleX = contentX;
      const bubbleY = y + nameH;
      ctx.fillStyle = '#fff';
      roundRect(ctx, bubbleX, bubbleY, layout.bubbleW, layout.bubbleH, 8);
      ctx.fill();
      ctx.fillStyle = '#1a1a1a';
      ctx.font = contentFont;
      ctx.textAlign = 'left'; ctx.textBaseline = 'middle';
      let textY = bubbleY + layout.bubbleH / 2 - (layout.wrappedLines.length - 1) * lineH / 2 + 1;
      for (const line of layout.wrappedLines) {
        ctx.fillText(line, bubbleX + bubblePadH, textY);
        textY += lineH;
      }
      y += layout.msgH + afterGap;
    } else {
      // Same sender, no avatar/name — just bubble with vertically centered text
      const bubbleX = contentX;
      const bubbleY = y;
      ctx.fillStyle = '#fff';
      roundRect(ctx, bubbleX, bubbleY, layout.bubbleW, layout.bubbleH, 8);
      ctx.fill();
      ctx.fillStyle = '#1a1a1a';
      ctx.font = contentFont;
      ctx.textAlign = 'left'; ctx.textBaseline = 'middle';
      let textY = bubbleY + layout.bubbleH / 2 - (layout.wrappedLines.length - 1) * lineH / 2 + 1;
      for (const line of layout.wrappedLines) {
        ctx.fillText(line, bubbleX + bubblePadH, textY);
        textY += lineH;
      }
      y += layout.msgH + afterGap;
    }
  }

  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => {
      if (blob) resolve(blob);
      else reject(new Error('canvas.toBlob returned null'));
    }, 'image/png');
  });
}

/**
 * Legacy fallback: copy image via contenteditable + execCommand('copy').
 * Works in non-secure contexts (HTTP non-localhost) where navigator.clipboard is undefined.
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

export const MemoryLibraryPage: React.FC<Props> = ({ contacts, groups }) => {
  const [facts, setFacts] = useState<MemFact[]>([]);
  const [contactStats, setContactStats] = useState<ContactStat[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [q, setQ] = useState('');
  const [activeContact, setActiveContact] = useState<string>(''); // '' = 全部
  const [pinnedOnly, setPinnedOnly] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [editDraft, setEditDraft] = useState('');
  // 手动添加
  const [addOpen, setAddOpen] = useState(false);
  const [addContact, setAddContact] = useState<string>('');
  const [addContactQuery, setAddContactQuery] = useState('');
  const [addFact, setAddFact] = useState('');
  const [addPinned, setAddPinned] = useState(false);
  const [addBusy, setAddBusy] = useState(false);
  const [addErr, setAddErr] = useState<string | null>(null);

  // hover 预览：hover 某条记忆时显示来源聊天记录
  const [hoverFactId, setHoverFactId] = useState<number | null>(null);
  const [hoverMsgs, setHoverMsgs] = useState<{ datetime: string; sender: string; content: string }[]>([]);
  const [hoverLoading, setHoverLoading] = useState(false);
  const [hoverPos, setHoverPos] = useState<{ top: number; left: number } | null>(null);
  const [hoverCopied, setHoverCopied] = useState(false);
  const [hoverShotLoading, setHoverShotLoading] = useState(false);
  const [hoverFactText, setHoverFactText] = useState('');
  const [hoverContactInfo, setHoverContactInfo] = useState<{ name: string; avatar?: string } | null>(null);
  const [hoverVisible, setHoverVisible] = useState(false);
  const showTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const fadeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // 用于把 contact_key 映射到头像 / 名称
  // 注意：后端 contact_key 带 contact:/group: 前缀，lookup 时要脱
  const stripKey = (k: string) => k.replace(/^(contact|group):/, '');
  const contactMap = useMemo(() => {
    const m = new Map<string, { name: string; avatar: string | undefined; isGroup: boolean }>();
    for (const c of contacts) {
      m.set(c.username, {
        name: c.remark || c.nickname || c.username,
        avatar: avatarSrc(c.small_head_url),
        isGroup: false,
      });
    }
    for (const g of groups) {
      m.set(g.username, {
        name: g.name || g.username,
        avatar: avatarSrc(g.small_head_url),
        isGroup: true,
      });
    }
    return m;
  }, [contacts, groups]);
  const lookup = (key: string) => contactMap.get(stripKey(key));

  // 所有人名集合（用于截图时高亮人名）
  const allNames = useMemo(() => {
    const s = new Set<string>();
    for (const c of contacts) {
      if (c.remark) s.add(c.remark);
      if (c.nickname) s.add(c.nickname);
    }
    s.add('我');
    return s;
  }, [contacts]);

  // sender name → avatar URL 映射（群聊里 sender 是显示名）
  const senderAvatarMap = useMemo(() => {
    const m = new Map<string, string | undefined>();
    for (const c of contacts) {
      const av = avatarSrc(c.small_head_url);
      if (c.remark) m.set(c.remark, av);
      if (c.nickname) m.set(c.nickname, av);
      m.set(c.username, av);
    }
    return m;
  }, [contacts]);

  const fetchFacts = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      if (activeContact) params.set('contact', activeContact);
      if (q.trim()) params.set('q', q.trim());
      if (pinnedOnly) params.set('pinned', '1');
      params.set('limit', '200');
      const r = await axios.get<{ facts: MemFact[]; total: number }>(`/api/memory/list?${params}`);
      setFacts(r.data.facts || []);
      setTotal(r.data.total || 0);
    } catch {
      setFacts([]);
      setTotal(0);
    } finally {
      setLoading(false);
    }
  }, [activeContact, q, pinnedOnly]);

  const fetchContactStats = useCallback(async () => {
    try {
      const r = await axios.get<{ contacts: ContactStat[] }>('/api/memory/contacts');
      setContactStats(r.data.contacts || []);
    } catch {
      setContactStats([]);
    }
  }, []);

  useEffect(() => { void fetchContactStats(); }, [fetchContactStats]);
  useEffect(() => {
    // 搜索 debounce
    const t = setTimeout(() => { void fetchFacts(); }, 200);
    return () => clearTimeout(t);
  }, [fetchFacts]);

  // hover 预览：hover 某条记忆时，debounce 300ms 后显示来源聊天记录
  const showPreview = (fact: MemFact, rect: DOMRect) => {
    if (hideTimer.current) { clearTimeout(hideTimer.current); hideTimer.current = null; }
    if (fadeTimer.current) { clearTimeout(fadeTimer.current); fadeTimer.current = null; }
    if (showTimer.current) clearTimeout(showTimer.current);
    showTimer.current = setTimeout(async () => {
      setHoverFactId(fact.id);
      const contactInfo = lookup(fact.contact_key);
      setHoverContactInfo(contactInfo ? { name: contactInfo.name, avatar: contactInfo.avatar } : null);
      // 初始标题：用当前 fact 的时间范围 + 内容
      const prefix = extractTimeRangePrefix(fact.fact);
      const timeRange = prefix ? prefix.trim() : '';
      const ownContent = prefix ? fact.fact.substring(prefix.length).trim() : fact.fact;
      setHoverFactText(timeRange + '\n- ' + ownContent);

      setHoverLoading(true);
      setHoverMsgs([]);

      // 动态定位：
      // - 元素在上半屏 → hover 框出现在下方，右上角贴元素右下角
      // - 元素在下半屏 → hover 框出现在上方，右下角贴元素右上角
      const POPUP_W = 460;
      const POPUP_H = 420;
      const GAP = 8;
      const vw = window.innerWidth;
      const vh = window.innerHeight;

      const elemCenterY = rect.top + rect.height / 2;
      const inUpperHalf = elemCenterY < vh / 2;

      // 右对齐到元素：hover 右边缘 = 元素右边缘
      let left = rect.right - POPUP_W;

      let top: number;
      if (inUpperHalf) {
        // 元素在上半屏 → hover 框出现在下方
        top = rect.bottom + GAP;
      } else {
        // 元素在下半屏 → hover 框出现在上方
        top = rect.top - POPUP_H - GAP;
      }

      // 溢出修正
      if (left + POPUP_W > vw) left = vw - POPUP_W - GAP;
      if (left < 0) left = GAP;
      if (top + POPUP_H > vh) top = vh - POPUP_H - GAP;
      if (top < 0) top = GAP;

      setHoverPos({ top, left });
      setHoverVisible(true);
      try {
        const r = await axios.get<{ messages: { datetime: string; sender: string; content: string }[]; related_facts?: string[] }>(
          `/api/memory/${fact.id}/source`,
        );
        setHoverMsgs(r.data.messages || []);
        // 用后端返回的同段事实构建完整标题（不受搜索结果 limit 限制）
        if (r.data.related_facts && r.data.related_facts.length > 0) {
          const rfPrefix = extractTimeRangePrefix(r.data.related_facts[0]);
          const rfTimeRange = rfPrefix ? rfPrefix.trim() : '';
          const contents = r.data.related_facts.map(f => {
            const p = extractTimeRangePrefix(f);
            return p ? f.substring(p.length).trim() : f;
          });
          setHoverFactText(rfTimeRange + '\n' + contents.map(c => '- ' + c).join('\n'));
        }
      } catch { /* ignore */ }
      finally { setHoverLoading(false); }
    }, 300);
  };

  const hidePreview = () => {
    if (showTimer.current) { clearTimeout(showTimer.current); showTimer.current = null; }
    if (hideTimer.current) clearTimeout(hideTimer.current);
    hideTimer.current = setTimeout(() => {
      setHoverVisible(false);
      if (fadeTimer.current) clearTimeout(fadeTimer.current);
      fadeTimer.current = setTimeout(() => {
        setHoverFactId(null);
        setHoverPos(null);
        setHoverMsgs([]);
      }, 150);
    }, 100);
  };

  const handleCopyAll = () => {
    const text = hoverMsgs.map(m => `[${m.datetime}] ${m.sender}: ${m.content}`).join('\n');
    navigator.clipboard.writeText(text);
    setHoverCopied(true);
    setTimeout(() => setHoverCopied(false), 2000);
  };

  const handleScreenshot = async () => {
    if (hoverMsgs.length === 0) return;
    setHoverShotLoading(true);
    try {
      const blob = await renderChatToBlob(
        hoverMsgs,
        (sender) => senderAvatarMap.get(sender),
        hoverFactText,
        hoverContactInfo || undefined,
        allNames,
      );

      let copied = false;

      // 方案 1: navigator.clipboard.write (需要安全上下文 HTTPS/localhost)
      if (typeof navigator !== 'undefined' && navigator.clipboard && typeof ClipboardItem !== 'undefined') {
        try {
          await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]);
          copied = true;
        } catch {
          // PNG 不被支持或权限被拒，继续尝试 legacy 方案
        }
      }

      // 方案 2: legacy execCommand('copy') (非安全上下文的回退)
      if (!copied) {
        copied = await copyImageViaExecCommand(blob);
      }

      if (copied) {
        setHoverCopied(true);
        setTimeout(() => setHoverCopied(false), 2000);
      } else {
        // 最终回退：在新标签页打开图片，用户可右键复制
        const url = URL.createObjectURL(blob);
        window.open(url, '_blank');
      }
    } catch (e) {
      console.error('Screenshot failed', e);
    } finally {
      setHoverShotLoading(false);
    }
  };

  // 清理定时器
  useEffect(() => {
    return () => {
      if (showTimer.current) clearTimeout(showTimer.current);
      if (hideTimer.current) clearTimeout(hideTimer.current);
    };
  }, []);

  const togglePin = async (f: MemFact) => {
    await axios.put(`/api/memory/${f.id}/pin`, { pinned: !f.pinned });
    setFacts(list => list.map(x => x.id === f.id ? { ...x, pinned: !x.pinned } : x));
    void fetchContactStats();
  };

  const deleteFact = async (f: MemFact) => {
    if (!confirm(`删除这条记忆？\n\n"${f.fact.slice(0, 60)}${f.fact.length > 60 ? '…' : ''}"`)) return;
    await axios.delete(`/api/memory/${f.id}`);
    setFacts(list => list.filter(x => x.id !== f.id));
    setTotal(t => Math.max(0, t - 1));
    void fetchContactStats();
  };

  const startEdit = (f: MemFact) => { setEditingId(f.id); setEditDraft(f.fact); };
  const cancelEdit = () => { setEditingId(null); setEditDraft(''); };
  const saveEdit = async (id: number) => {
    const v = editDraft.trim();
    if (!v) return;
    await axios.put(`/api/memory/${id}`, { fact: v });
    setFacts(list => list.map(x => x.id === id ? { ...x, fact: v } : x));
    cancelEdit();
  };

  const totalAll = contactStats.reduce((s, c) => s + c.count, 0);
  const pinnedAll = contactStats.reduce((s, c) => s + c.pinned_count, 0);

  // 搜索框同时按"人名"过滤左栏：输入命中联系人名字/备注时，左栏只剩匹配项
  // （fact 内容过滤依然走后端 ?q=；两者并存，搜 "生日 alice" 这种也能工作）
  const qLower = q.trim().toLowerCase();
  const filteredContactStats = useMemo(() => {
    if (!qLower) return contactStats;
    return contactStats.filter(s => {
      const info = lookup(s.contact_key);
      const name = (info?.name || stripKey(s.contact_key)).toLowerCase();
      return name.includes(qLower) || s.contact_key.toLowerCase().includes(qLower);
    });
  }, [contactStats, qLower, contactMap]);

  const openAdd = () => {
    setAddContact(activeContact || '');
    setAddContactQuery('');
    setAddFact('');
    setAddPinned(false);
    setAddErr(null);
    setAddOpen(true);
  };
  const handleAdd = async () => {
    if (!addContact || !addFact.trim()) { setAddErr('请选择联系人并填写事实'); return; }
    setAddBusy(true); setAddErr(null);
    try {
      await axios.post('/api/memory', {
        contact_key: addContact,
        fact: addFact.trim(),
        pinned: addPinned,
      });
      setAddOpen(false);
      await fetchContactStats();
      await fetchFacts();
    } catch (e: unknown) {
      const anyE = e as { response?: { data?: { error?: string } }; message?: string };
      setAddErr(anyE?.response?.data?.error || anyE?.message || '添加失败');
    } finally {
      setAddBusy(false);
    }
  };
  // 添加 modal 的联系人候选：所有 contacts + groups，按查询过滤
  const addCandidates = useMemo(() => {
    const q = addContactQuery.trim().toLowerCase();
    const all: { key: string; name: string; avatar?: string; isGroup: boolean }[] = [];
    for (const c of contacts) {
      all.push({
        key: 'contact:' + c.username,
        name: c.remark || c.nickname || c.username,
        avatar: avatarSrc(c.small_head_url),
        isGroup: false,
      });
    }
    for (const g of groups) {
      all.push({
        key: 'group:' + g.username,
        name: g.name || g.username,
        avatar: avatarSrc(g.small_head_url),
        isGroup: true,
      });
    }
    if (!q) return all.slice(0, 50);
    return all.filter(x => x.name.toLowerCase().includes(q) || x.key.toLowerCase().includes(q)).slice(0, 50);
  }, [contacts, groups, addContactQuery]);

  return (
    <div className="p-4 sm:p-10 pb-20">
      <header className="mb-6 flex items-center gap-3 flex-wrap">
        <div className="w-10 h-10 rounded-2xl bg-gradient-to-br from-[#07c160] to-[#06ad56] flex items-center justify-center shadow-md">
          <Brain size={22} className="text-white" />
        </div>
        <div className="flex-1 min-w-0">
          <h1 className="text-2xl font-black tracking-tight dk-text">记忆库</h1>
          <p className="text-sm text-gray-400 mt-0.5">
            共 {totalAll} 条 AI 提炼事实 · {pinnedAll} 条已置顶（AI 对话自动引用）
          </p>
        </div>
        <button
          onClick={openAdd}
          className="flex items-center gap-1.5 px-4 py-2 rounded-xl bg-[#07c160] text-white text-sm font-semibold hover:bg-[#06ad56] transition-colors"
        >
          <Plus size={14} />
          手动添加
        </button>
      </header>

      <JobProgressPanel contacts={contacts} groups={groups} />
      <BatchExtractPanel contacts={contacts} groups={groups} />

      <div className="flex gap-4 flex-col lg:flex-row">
        {/* 左栏：联系人筛选 */}
        <aside className="lg:w-64 shrink-0 bg-white dark:bg-[#1d1d1f] rounded-2xl border border-gray-100 dark:border-white/10 p-3 h-fit">
          <button
            onClick={() => setActiveContact('')}
            className={`w-full text-left px-3 py-2 rounded-xl text-sm font-medium flex items-center justify-between transition-colors ${
              activeContact === '' ? 'bg-[#07c160]/10 text-[#07c160]' : 'hover:bg-gray-50 dark:hover:bg-white/5 text-gray-700 dark:text-gray-300'
            }`}
          >
            <span>全部</span>
            <span className="text-xs text-gray-400">{totalAll}</span>
          </button>
          <div className="mt-2 max-h-[calc(100vh-320px)] overflow-y-auto space-y-0.5">
            {qLower && filteredContactStats.length === 0 && (
              <div className="text-xs text-gray-400 px-2 py-2">没有联系人名字匹配「{q}」</div>
            )}
            {filteredContactStats.map(s => {
              const info = lookup(s.contact_key);
              const active = activeContact === s.contact_key;
              return (
                <button
                  key={s.contact_key}
                  onClick={() => setActiveContact(s.contact_key)}
                  className={`w-full text-left px-2 py-1.5 rounded-xl text-sm flex items-center gap-2 transition-colors ${
                    active ? 'bg-[#07c160]/10 text-[#07c160]' : 'hover:bg-gray-50 dark:hover:bg-white/5 text-gray-700 dark:text-gray-300'
                  }`}
                >
                  {info ? (
                    <img src={info.avatar} alt="" className="w-6 h-6 rounded-lg object-cover shrink-0" />
                  ) : (
                    <div className="w-6 h-6 rounded-lg bg-gray-200 dark:bg-white/10 shrink-0" />
                  )}
                  <span className="truncate flex-1">{info?.name || stripKey(s.contact_key)}</span>
                  {s.pinned_count > 0 && (
                    <span className="text-[10px] text-amber-500">📌{s.pinned_count}</span>
                  )}
                  <span className="text-xs text-gray-400">{s.count}</span>
                </button>
              );
            })}
          </div>
        </aside>

        {/* 右栏：搜索 + 事实列表 */}
        <main className="flex-1 min-w-0">
          <div className="bg-white dark:bg-[#1d1d1f] rounded-2xl border border-gray-100 dark:border-white/10 p-3 mb-3 flex items-center gap-2 flex-wrap">
            <div className="flex items-center gap-2 flex-1 min-w-[200px]">
              <Search size={16} className="text-gray-400 shrink-0" />
              <input
                value={q}
                onChange={e => setQ(e.target.value)}
                placeholder="搜索记忆内容或联系人..."
                className="flex-1 bg-transparent outline-none text-sm dk-text placeholder-gray-400"
              />
              {q && (
                <button onClick={() => setQ('')} className="text-gray-400 hover:text-gray-600">
                  <XIcon size={14} />
                </button>
              )}
            </div>
            <label className="flex items-center gap-1.5 text-xs text-gray-600 dark:text-gray-400 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={pinnedOnly}
                onChange={e => setPinnedOnly(e.target.checked)}
                className="accent-[#07c160]"
              />
              只看置顶
            </label>
            <span className="text-xs text-gray-400">{total} 条</span>
          </div>

          {loading ? (
            <div className="py-12 text-center text-gray-400">
              <Loader2 size={20} className="animate-spin inline" />
            </div>
          ) : facts.length === 0 ? (
            <div className="bg-white dark:bg-[#1d1d1f] rounded-2xl border border-gray-100 dark:border-white/10 p-12 text-center">
              <Brain size={36} className="text-gray-300 dark:text-white/20 mx-auto mb-3" />
              <p className="text-sm text-gray-500">
                {q ? '没有匹配的记忆' : activeContact ? '这个联系人还没有提炼的记忆' : '还没有记忆。到联系人详情页开启「记忆提炼」功能即可'}
              </p>
            </div>
          ) : (
            <div className="space-y-2">
              {facts.map(f => {
                const info = lookup(f.contact_key);
                const isEditing = editingId === f.id;
                return (
                  <div
                    key={f.id}
                    className={`bg-white dark:bg-[#1d1d1f] rounded-2xl border p-4 transition-colors ${
                      f.pinned ? 'border-amber-300 dark:border-amber-500/40' : 'border-gray-100 dark:border-white/10'
                    }`}
                    onMouseEnter={(e) => showPreview(f, e.currentTarget.getBoundingClientRect())}
                    onMouseLeave={() => hidePreview()}
                  >
                    <div className="flex items-start gap-3">
                      {info ? (
                        <img src={info.avatar} alt="" className="w-8 h-8 rounded-xl object-cover shrink-0" title={info.name} />
                      ) : (
                        <div className="w-8 h-8 rounded-xl bg-gray-200 dark:bg-white/10 shrink-0" />
                      )}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2 mb-1 flex-wrap">
                          <span className="text-xs font-semibold text-gray-600 dark:text-gray-400">
                            {info?.name || stripKey(f.contact_key)}
                          </span>
                          {f.pinned && (
                            <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-100 dark:bg-amber-900/30 text-amber-700 dark:text-amber-400 font-semibold">
                              📌 置顶 · AI 对话自动引用
                            </span>
                          )}
                          {f.updated_at ? (
                            <span className="text-[10px] text-gray-400">
                              更新于 <RelativeTime ts={f.updated_at} />
                            </span>
                          ) : null}
                        </div>
                        {isEditing ? (
                          <>
                            <textarea
                              value={editDraft}
                              onChange={e => setEditDraft(e.target.value)}
                              rows={3}
                              autoFocus
                              className="w-full px-3 py-2 rounded-xl bg-gray-50 dark:bg-white/5 border border-gray-200 dark:border-white/10 text-sm dk-text outline-none focus:border-[#07c160]"
                            />
                            <div className="flex gap-2 mt-2">
                              <button onClick={() => saveEdit(f.id)} className="px-3 py-1 rounded-lg bg-[#07c160] text-white text-xs font-semibold hover:bg-[#06ad56] flex items-center gap-1">
                                <Check size={12} />保存
                              </button>
                              <button onClick={cancelEdit} className="px-3 py-1 rounded-lg bg-gray-100 dark:bg-white/10 text-gray-600 dark:text-gray-300 text-xs font-semibold">
                                取消
                              </button>
                            </div>
                          </>
                        ) : (
                          <p className="text-sm text-gray-800 dark:text-gray-200 leading-relaxed break-words">
                            {f.fact}
                          </p>
                        )}
                      </div>
                      {!isEditing && (
                        <div className="flex gap-1 shrink-0">
                          <button
                            onClick={() => togglePin(f)}
                            title={f.pinned ? '取消置顶' : '置顶'}
                            className={`p-1.5 rounded-lg transition-colors ${
                              f.pinned
                                ? 'text-amber-500 hover:bg-amber-50 dark:hover:bg-amber-900/20'
                                : 'text-gray-400 hover:bg-gray-100 dark:hover:bg-white/5'
                            }`}
                          >
                            {f.pinned ? <PinOff size={14} /> : <Pin size={14} />}
                          </button>
                          <button
                            onClick={() => startEdit(f)}
                            title="编辑"
                            className="p-1.5 rounded-lg text-gray-400 hover:bg-gray-100 dark:hover:bg-white/5"
                          >
                            <Pencil size={14} />
                          </button>
                          <button
                            onClick={() => deleteFact(f)}
                            title="删除"
                            className="p-1.5 rounded-lg text-gray-400 hover:text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20"
                          >
                            <Trash2 size={14} />
                          </button>
                        </div>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </main>
      </div>

      {/* 手动添加记忆 modal */}
      {addOpen && (
        <div className="fixed inset-0 z-[9000] flex items-center justify-center bg-black/40 p-4" onClick={() => !addBusy && setAddOpen(false)}>
          <div className="w-full max-w-lg rounded-3xl bg-white dark:bg-[#1d1d1f] shadow-2xl border border-gray-100 dark:border-white/10 p-6" onClick={(e) => e.stopPropagation()}>
            <h3 className="text-base font-bold dk-text mb-3">添加一条记忆</h3>
            <p className="text-xs text-gray-400 mb-4">手动添加的事实会参与 AI 对话上下文检索；勾选置顶后无论相似度都会被引用。</p>

            {/* 联系人 */}
            <label className="text-xs text-gray-600 dark:text-gray-400 font-semibold">关联到</label>
            {addContact ? (
              <div className="mt-1 flex items-center justify-between gap-2 px-3 py-2 rounded-xl bg-gray-50 dark:bg-white/5 border border-gray-200 dark:border-white/10">
                {(() => {
                  const info = lookup(addContact);
                  return (
                    <div className="flex items-center gap-2 min-w-0">
                      {info?.avatar
                        ? <img src={info.avatar} alt="" className="w-6 h-6 rounded-lg object-cover" />
                        : <div className="w-6 h-6 rounded-lg bg-gray-200 dark:bg-white/10" />}
                      <span className="text-sm dk-text truncate">{info?.name || stripKey(addContact)}</span>
                    </div>
                  );
                })()}
                <button onClick={() => setAddContact('')} className="text-gray-400 hover:text-gray-600">
                  <XIcon size={14} />
                </button>
              </div>
            ) : (
              <>
                <input
                  value={addContactQuery}
                  onChange={(e) => setAddContactQuery(e.target.value)}
                  placeholder="搜索联系人或群聊..."
                  className="w-full mt-1 px-3 py-2 rounded-xl bg-gray-50 dark:bg-white/5 border border-gray-200 dark:border-white/10 text-sm dk-text outline-none focus:border-[#07c160]"
                />
                <div className="mt-2 max-h-48 overflow-y-auto space-y-0.5 border border-gray-100 dark:border-white/5 rounded-xl p-1">
                  {addCandidates.length === 0 ? (
                    <div className="text-xs text-gray-400 px-2 py-3 text-center">没有匹配的联系人</div>
                  ) : addCandidates.map(x => (
                    <button
                      key={x.key}
                      onClick={() => { setAddContact(x.key); setAddContactQuery(''); }}
                      className="w-full text-left px-2 py-1.5 rounded-lg hover:bg-gray-50 dark:hover:bg-white/5 flex items-center gap-2 text-sm"
                    >
                      {x.avatar ? <img src={x.avatar} alt="" className="w-6 h-6 rounded-lg object-cover" /> : <div className="w-6 h-6 rounded-lg bg-gray-200 dark:bg-white/10" />}
                      <span className="dk-text truncate flex-1">{x.name}</span>
                      {x.isGroup && <span className="text-[10px] text-gray-400">群</span>}
                    </button>
                  ))}
                </div>
              </>
            )}

            {/* 事实内容 */}
            <label className="text-xs text-gray-600 dark:text-gray-400 font-semibold block mt-4">事实内容</label>
            <textarea
              value={addFact}
              onChange={(e) => setAddFact(e.target.value)}
              rows={3}
              placeholder="例如：对方的生日是 10 月 15 日；家住在北京朝阳区"
              className="w-full mt-1 px-3 py-2 rounded-xl bg-gray-50 dark:bg-white/5 border border-gray-200 dark:border-white/10 text-sm dk-text outline-none focus:border-[#07c160]"
            />

            <label className="flex items-center gap-2 mt-3 text-xs text-gray-600 dark:text-gray-400 cursor-pointer select-none">
              <input type="checkbox" checked={addPinned} onChange={(e) => setAddPinned(e.target.checked)} className="accent-[#07c160]" />
              置顶（AI 对话始终引用）
            </label>

            {addErr && <p className="mt-2 text-xs text-red-500">{addErr}</p>}

            <div className="mt-5 flex gap-2 justify-end">
              <button onClick={() => setAddOpen(false)} disabled={addBusy} className="px-4 py-2 rounded-xl bg-gray-100 dark:bg-white/10 text-gray-600 dark:text-gray-300 text-sm">取消</button>
              <button onClick={handleAdd} disabled={addBusy} className="px-4 py-2 rounded-xl bg-[#07c160] text-white text-sm font-semibold disabled:opacity-50 flex items-center gap-1.5">
                {addBusy && <Loader2 size={14} className="animate-spin" />}
                添加
              </button>
            </div>
          </div>
        </div>
      )}

      {/* hover 预览浮层 */}
      {hoverFactId !== null && hoverPos && (
        <div
          className={`fixed z-[8000] w-[460px] max-h-[420px] rounded-2xl bg-white dark:bg-[#1d1d1f] shadow-2xl border border-gray-200 dark:border-white/10 flex flex-col overflow-hidden transition-opacity duration-150 ${hoverVisible ? 'opacity-100' : 'opacity-0'}`}
          style={{ top: hoverPos.top, left: hoverPos.left }}
          onMouseEnter={() => {
            if (hideTimer.current) { clearTimeout(hideTimer.current); hideTimer.current = null; }
            if (fadeTimer.current) { clearTimeout(fadeTimer.current); fadeTimer.current = null; }
            setHoverVisible(true);
          }}
          onMouseLeave={() => hidePreview()}
        >
          <div className="flex items-center justify-between px-3 py-2 border-b border-gray-100 dark:border-white/10 shrink-0">
            <span className="text-xs font-semibold text-gray-500 dark:text-gray-400">来源聊天记录</span>
            <div className="flex items-center gap-3">
              <button
                onClick={handleScreenshot}
                disabled={hoverShotLoading || hoverMsgs.length === 0}
                className="flex items-center gap-1 text-xs text-gray-400 hover:text-[#07c160] transition-colors disabled:opacity-50"
              >
                {hoverShotLoading ? <Loader2 size={12} className="animate-spin" /> : <Camera size={12} />}
                截图
              </button>
              <button
                onClick={handleCopyAll}
                className="flex items-center gap-1 text-xs text-gray-400 hover:text-[#07c160] transition-colors"
              >
                {hoverCopied ? <Check size={12} /> : <Copy size={12} />}
                {hoverCopied ? '已复制' : '复制全部'}
              </button>
            </div>
          </div>
          <div className="overflow-y-auto p-2 space-y-1 flex-1">
            {hoverLoading ? (
              <div className="py-4 text-center">
                <Loader2 size={16} className="animate-spin inline text-gray-400" />
              </div>
            ) : hoverMsgs.length === 0 ? (
              <div className="py-4 text-center text-xs text-gray-400">无来源聊天记录</div>
            ) : (
              hoverMsgs.map((m, idx) => (
                <div key={idx} className="text-[11px] leading-relaxed break-words">
                  <span className="text-gray-400">{m.datetime}</span>{' '}
                  <span className="text-gray-600 dark:text-gray-300 font-medium">{m.sender}:</span>{' '}
                  <span className="text-gray-700 dark:text-gray-200">{m.content}</span>
                </div>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
};
