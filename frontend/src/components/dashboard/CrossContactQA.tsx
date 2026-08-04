/**
 * 跨联系人 AI 问答 — Agent 模式：LLM 解析意图 → 自动搜索/查询 → LLM 汇总回答
 * 支持问题如："谁跟我聊过旅行""去年国庆和谁聊天了""哪些朋友经常提到加班"
 */

import React, { useState, useRef, useCallback, useEffect, useMemo } from 'react';
import { Globe, Send, Loader2, Trash2, Bot, Search, Calendar, RotateCcw, Check, Copy, Camera, X } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { generateAIScreenshot } from '../../utils/shareImage';
import { truncateMsgContent } from '../../utils/formatters';
import { preserveMarkdownBlockquote } from '../../utils/formatters';
import type { ChatMessage } from '../../types';
import { usePrivacyMode } from '../../contexts/PrivacyModeContext';
import { ConversationHistory } from './ConversationHistory';

interface Props {
  onOpenSettings?: () => void;
  onContactClick?: (username: string) => void;
  onGroupClick?: (username: string) => void;
}

interface SearchHit {
  display_name: string;
  username: string;
  is_group: boolean;
  count: number;
  messages?: ChatMessage[];
}

// ── 记忆优先两级检索的响应类型 ──
interface SourceMessage {
  seq: number;
  datetime: string;
  sender: string;
  content: string;
}

interface MemFact {
  id?: number;
  contact_key?: string;
  fact: string;
  source_from: number;
  source_to: number;
  pinned?: boolean;
  created_at?: number;
  updated_at?: number;
}

interface FactSource {
  fact: MemFact;
  messages: SourceMessage[];
  source_name?: string;
}

interface ResolvedEntity {
  name: string;
  contact_key: string;
  display_name: string;
  is_group: boolean;
}

interface QueryDecomposition {
  needs_memory: boolean;
  entities: string[];
  concepts: string[];
  time_from: string;
  time_to: string;
  groups?: string[];
}

interface StreamUsage {
  prompt_tokens: number;
  output_tokens: number;
  total_tokens: number;
}

interface LLMMessage {
  role: string;
  content: string;
}

interface VecMessageHit {
  contact_key: string;
  seq: number;
  datetime: string;
  sender: string;
  content: string;
  similarity: number;
}

interface MemorySearchResponse {
  decomposition: QueryDecomposition;
  resolved_entities: ResolvedEntity[];
  facts: MemFact[];
  sources: FactSource[];
  pinned_facts: MemFact[];
  token_usage?: StreamUsage;
  decompose_prompt?: LLMMessage[];
  // 增强检索结果
  vec_messages?: VecMessageHit[];
  expanded_queries?: string[];
  rerank_used?: boolean;
  vector_hits?: number;
  bm25_hits?: number;
  vec_message_hits?: number;
  rerank_results?: RerankScoreItem[];
  raw_hits?: RawExcerpt[];
}

interface RerankScoreItem {
  score: number;
  text: string;
}

// 找原文场景：原始聊天记录精确命中
interface RawExcerpt {
  source_name: string;
  datetime: string;
  sender: string;
  content: string;
  seq?: number;
}

interface ProgressStep {
  step: string;
  detail: string;
  timestamp: number;
}

// 进度步骤映射：将后端的 step 名称映射为可读的步骤编号和标签
const STEP_MAP: Record<string, { index: number; total: number; label: string }> = {
  decompose:        { index: 1, total: 4, label: '分解问题' },
  resolve_entities: { index: 1, total: 4, label: '解析实体' },
  query_expansion:  { index: 2, total: 4, label: '扩展查询' },
  vector_search:    { index: 2, total: 4, label: '向量检索记忆' },
  bm25_search:      { index: 2, total: 4, label: 'BM25关键词检索' },
  vecmsg_search:    { index: 2, total: 4, label: '搜索原始聊天记录embedding' },
  expanded_search:  { index: 2, total: 4, label: '扩展子查询检索' },
  search_facts:     { index: 2, total: 4, label: '向量检索记忆事实' },
  rrf_fusion:       { index: 3, total: 4, label: '融合排序' },
  rerank:           { index: 3, total: 4, label: '精排候选' },
  extract_sources:  { index: 3, total: 4, label: '提取源聊天记录' },
};

// 将 ProgressStep 格式化为 "Step [1/4] 标签 | [2/5]" 的形式
function formatProgressStep(ps: ProgressStep): string {
  const meta = STEP_MAP[ps.step];
  if (!meta) return ps.detail;
  // 从 detail 中提取 [current/total] 子进度
  const subMatch = ps.detail.match(/^\[(\d+)\/(\d+)\]/);
  const subProgress = subMatch ? ` | ${subMatch[0]}` : '';
  return `Step [${meta.index}/${meta.total}] ${meta.label}${subProgress}`;
}

// 最新 3 条的滚动进度条：
// 窗口固定 3 行，当第 4 条出现时，整体上移一行、最老一条淡出，
// 最新一条从下方淡入。内部用 FLIP 式过渡（先记录上一长度，再在渲染后
// 把轨道从 -20px 平移到 0），让滑动/淡入淡出连贯。
const MAX_VISIBLE_STEPS = 3;
function ProgressTicker({ steps }: { steps: ProgressStep[] }) {
  const id = React.useId().replace(/:/g, '');
  // 窗口固定 3 行：不足 3 条时内容贴底显示；超过 3 条时最新 3 条可见，
  // 最老一条淡出（被挤出顶部）、最新一条从下方淡入。
  const raw = steps.slice(-(MAX_VISIBLE_STEPS + 1));
  const overflow = steps.length > MAX_VISIBLE_STEPS;

  return (
    <div className="mt-1.5 pl-1">
      <style>{`
        .ticker-${id} { position: relative; height: ${MAX_VISIBLE_STEPS * 20}px; overflow: hidden;
          display: flex; flex-direction: column; justify-content: flex-end; }
        .ticker-${id} .ticker-track { display: flex; flex-direction: column; width: 100%; }
        .ticker-${id} .row { display: flex; align-items: center; gap: 6px; height: 20px; line-height: 20px;
          opacity: 1; transform: translateY(0); flex-shrink: 0; }
        .ticker-${id} .row.enter { opacity: 0; animation: ticker-in-${id} 0.3s ease forwards; }
        .ticker-${id} .row.leave { opacity: 0; transition: opacity 0.28s ease; }
        @keyframes ticker-in-${id} { from { opacity: 0; transform: translateY(10px); }
          to { opacity: 1; transform: translateY(0); } }
      `}</style>
      <div className={`ticker-${id}`}>
        {raw.map((ps, idx) => {
          const isOldest = idx === 0 && overflow && steps.length > 1;
          const isNewest = idx === raw.length - 1;
          return (
            <div key={ps.timestamp + '-' + idx}
                 className={`row ${isOldest ? 'leave' : ''} ${isNewest ? 'enter' : ''}`}>
              <span className="text-gray-300 text-[9px]">✓</span>
              <span className="break-all text-[10px] text-gray-400">{formatProgressStep(ps)}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

interface Message {
  role: 'user' | 'assistant' | 'system';
  content: string;
  tool?: string;
  searching?: boolean;
  searchHits?: SearchHit[]; // 完整搜索结果（用于展示在 AI 回答下方）
  tokenUsage?: StreamUsage; // 本次提问+回答消耗的 token
  elapsedMs?: number; // 本次提问+回答的耗时（毫秒）
  memorySearchData?: MemorySearchResponse; // 记忆检索详情（下拉框展示）
  llmPrompt?: LLMMessage[]; // 最终发给 LLM API 的原始 prompt
  progressSteps?: ProgressStep[]; // 思考过程步骤列表
  thinking?: string; // LLM 思考过程（思考型模型）
}

function formatTokens(n: number): string {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
  return n.toString();
}

// 只保留检索详情中“轻量”字段，丢弃所有大体积内容（source 消息、
// 原文命中、向量命中、rerank 明细、完整 prompt、扩展子查询）。
// 找原文追问已改为由后端按会话 key 从 ai_conversations 读回原文候选，
// 前端不再需要在内存里保留这些原始内容。
function compactMemorySearchData(d: MemorySearchResponse): MemorySearchResponse | undefined {
  if (!d) return undefined;
  return {
    decomposition: d.decomposition,
    resolved_entities: d.resolved_entities,
    // facts / sources 是类型必填字段，压缩后用空数组占位，不保留源消息原文。
    facts: [],
    sources: [],
    pinned_facts: d.pinned_facts,
    token_usage: d.token_usage,
    rerank_used: d.rerank_used,
    vector_hits: d.vector_hits,
    bm25_hits: d.bm25_hits,
    vec_message_hits: d.vec_message_hits,
  };
}

// 检索详情的折叠面板。核心点：内容只有在用户真正打开时才挂载/渲染，
// 避免每条 AI 回答都把几十上百条 sources / rerank / 原文渲染进隐藏 DOM，
// 造成输入时的持续卡顿。
const RetrievalDetails: React.FC<{
  msg: Message;
  privacyMode: boolean;
  conversationKey?: string;
  onAskRaw?: (question: string) => void;
}> = ({ msg, privacyMode, conversationKey, onAskRaw }) => {
  const [open, setOpen] = useState(false);
  const [candidates, setCandidates] = useState<RawExcerpt[] | null>(null);
  const [candLoading, setCandLoading] = useState(false);

  // 打开时才从后端按会话拉取原文候选，避免占用前端内存。
  useEffect(() => {
    if (!open || !conversationKey || candidates !== null) return;
    let cancelled = false;
    setCandLoading(true);
    fetch(`/api/ai/conversations/candidates?key=${encodeURIComponent(conversationKey)}`)
      .then(r => r.json())
      .then((d: { candidates?: RawExcerpt[] }) => {
        if (!cancelled) setCandidates(d.candidates ?? []);
      })
      .catch(() => { if (!cancelled) setCandidates([]); })
      .finally(() => { if (!cancelled) setCandLoading(false); });
    return () => { cancelled = true; };
  }, [open, conversationKey, candidates]);

  return (
    <details open={open} onToggle={e => setOpen((e.target as HTMLDetailsElement).open)} className="mt-1.5 w-full">
      <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-[#07c160] transition-colors select-none flex items-center gap-1">
        <Search size={10} />
        检索详情
      </summary>
      {open && (
        <div className="mt-2 p-3 bg-gray-50 dark:bg-white/5 rounded-xl text-xs space-y-3">
          {/* 查询分解 */}
          {msg.memorySearchData?.decomposition && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">查询分解</div>
              <div className="space-y-0.5 text-gray-500">
                <div>需要检索记忆: {msg.memorySearchData.decomposition.needs_memory ? '是' : '否（可即答）'}</div>
                {msg.memorySearchData.decomposition.entities?.length > 0 && (
                  <div>实体: {msg.memorySearchData.decomposition.entities.join('、')}</div>
                )}
                {msg.memorySearchData.decomposition.concepts?.length > 0 && (
                  <div>概念: {msg.memorySearchData.decomposition.concepts.join('、')}</div>
                )}
                {(msg.memorySearchData.decomposition.time_from || msg.memorySearchData.decomposition.time_to) && (
                  <div>时间范围: {msg.memorySearchData.decomposition.time_from || '?'} ~ {msg.memorySearchData.decomposition.time_to || '?'}</div>
                )}
              </div>
            </div>
          )}
          {/* 解析实体 */}
          {msg.memorySearchData?.resolved_entities && msg.memorySearchData.resolved_entities.length > 0 && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">解析实体</div>
              <div className="space-y-0.5 text-gray-500">
                {msg.memorySearchData.resolved_entities.map((re, idx) => (
                  <div key={idx}>
                    {re.name} → {re.contact_key || '未匹配'} {re.display_name ? `(${re.display_name})` : ''}
                  </div>
                ))}
              </div>
            </div>
          )}
          {/* 用户指定的群聊 */}
          {msg.memorySearchData?.decomposition?.groups && msg.memorySearchData.decomposition.groups.length > 0 && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">指定群聊</div>
              <div className="text-gray-500">{msg.memorySearchData.decomposition.groups.join('、')}</div>
            </div>
          )}
          {/* 记忆事实（不展示源聊天记录） */}
          {msg.memorySearchData?.sources && msg.memorySearchData.sources.length > 0 && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">
                检索到 {msg.memorySearchData.sources.length} 条记忆事实
              </div>
              <div className="space-y-1">
                {msg.memorySearchData.sources.map((src, idx) => (
                  <div key={idx} className="border-l-2 border-gray-200 dark:border-gray-700 pl-2">
                    <span className="text-gray-500 text-[10px]">{privacyMode ? '***' : (src.source_name || src.fact?.contact_key || '未知')}</span>
                    <div className="text-gray-600 dark:text-gray-300 text-xs">{src.fact.fact}</div>
                  </div>
                ))}
              </div>
            </div>
          )}
          {/* 置顶事实 */}
          {msg.memorySearchData?.pinned_facts && msg.memorySearchData.pinned_facts.length > 0 && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">置顶事实</div>
              <div className="space-y-0.5 text-gray-500">
                {msg.memorySearchData.pinned_facts.map((pf, idx) => (
                  <div key={idx}>- {pf.fact}</div>
                ))}
              </div>
            </div>
          )}
          {/* 原文精确命中（找原文场景）— 数据来自后端会话，按需拉取 */}
          <div>
            <div className="flex items-center justify-between mb-1">
              <div className="font-semibold text-gray-600 dark:text-gray-300">聊天记录原文（找原文）</div>
              {onAskRaw && (
                <button
                  onClick={() => { setOpen(false); onAskRaw('把上面这段聊天记录对应的原文原样贴出来，并标注时间和来源。'); }}
                  className="text-[10px] px-2 py-0.5 rounded-lg bg-[#07c160]/10 text-[#07c160] hover:bg-[#e7f8f0] transition-colors"
                >
                  问 AI：贴出这段原文
                </button>
              )}
            </div>
            {candLoading ? (
              <div className="text-[10px] text-gray-400">正在加载原文...</div>
            ) : candidates && candidates.length > 0 ? (
              <div className="mt-1 space-y-1 max-h-56 overflow-y-auto">
                {candidates.map((rh, idx) => (
                  <div key={idx} className="border-l-2 border-gray-200 dark:border-gray-700 pl-2">
                    <span className="text-gray-500 text-[10px]">{privacyMode ? '***' : rh.source_name} · {rh.datetime} {rh.sender}</span>
                    <div className="text-gray-600 dark:text-gray-300 text-xs break-all">{privacyMode ? '***' : rh.content}</div>
                  </div>
                ))}
              </div>
            ) : candidates ? (
              <div className="text-[10px] text-gray-400">当前会话暂无已保存的原文候选，可点上方按钮让 AI 去检索。</div>
            ) : null}
          </div>
          {/* 增强检索统计 */}
          {msg.memorySearchData && (msg.memorySearchData.vector_hits || msg.memorySearchData.bm25_hits || msg.memorySearchData.vec_message_hits || msg.memorySearchData.rerank_used !== undefined) && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">检索统计</div>
              <div className="space-y-0.5 text-gray-500">
                <div>向量检索: {msg.memorySearchData.vector_hits ?? 0} 条命中</div>
                <div>BM25 检索: {msg.memorySearchData.bm25_hits ?? 0} 条命中</div>
                <div>原始消息检索: {msg.memorySearchData.vec_message_hits ?? 0} 条命中</div>
                <div>Rerank 精排: {msg.memorySearchData.rerank_used ? '✅ 已使用' : '❌ 未使用'}</div>
              </div>
            </div>
          )}
          {/* 双路检索：原始消息命中 */}
          {msg.memorySearchData?.vec_messages && msg.memorySearchData.vec_messages.length > 0 && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">
                原始消息检索命中（{msg.memorySearchData.vec_messages.length} 条）
              </div>
              <div className="mt-1 space-y-1 max-h-56 overflow-y-auto">
                {msg.memorySearchData.vec_messages.map((vm, idx) => (
                  <div key={idx} className="border-l-2 border-gray-200 dark:border-gray-700 pl-2">
                    <span className="text-gray-500 text-[10px]">{vm.datetime} {vm.sender}</span>
                    <div className="text-gray-600 dark:text-gray-300 text-xs">{vm.content}</div>
                  </div>
                ))}
              </div>
            </div>
          )}
          {/* 发给 LLM 的原始文本 */}
          {msg.llmPrompt && msg.llmPrompt.length > 0 && (
            <div>
              <div className="font-semibold text-gray-600 dark:text-gray-300 mb-1">发给 LLM 的原始文本</div>
              <div className="space-y-2">
                {msg.llmPrompt.map((m, idx) => (
                  <div key={idx} className="border-l-2 border-gray-200 dark:border-gray-700 pl-2">
                    <span className="text-[10px] font-semibold text-gray-400">{m.role}</span>
                    <pre className="text-xs text-gray-600 dark:text-gray-300 whitespace-pre-wrap break-words font-mono mt-1">{m.content}</pre>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </details>
  );
};

const EXAMPLE_QUESTIONS = [
  '谁跟我聊过旅行？',
  '去年国庆我都跟谁聊天了？',
  '哪些朋友经常提到加班？',
  '最近一个月谁给我发了红包？',
  '有没有人跟我聊过买房？',
  '谁经常在深夜找我聊天？',
];

export const CrossContactQA: React.FC<Props> = ({ onOpenSettings, onContactClick, onGroupClick }) => {
  const { privacyMode } = usePrivacyMode();
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState('');
  const [loading, setLoading] = useState(false);
  const [profileId, setProfileId] = useState('');
  const [profiles, setProfiles] = useState<{ id: string; provider: string; model?: string }[]>([]);

  const [sharedIdx, setSharedIdx] = useState(-1);
  const [shotLoadingIdx, setShotLoadingIdx] = useState(-1);
  const [shotDoneIdx, setShotDoneIdx] = useState(-1);
  const [popupHit, setPopupHit] = useState<SearchHit | null>(null);
  const [conversationKey, setConversationKey] = useState<string | null>(null);
  const [copiedIdx, setCopiedIdx] = useState(-1);
  const scrollRef = useRef<HTMLDivElement>(null);
  const followStreamRef = useRef(true);
  const lastScrollTopRef = useRef(0);
  const autoScrollingRef = useRef(false);
  const autoScrollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const collapseAllDetails = useCallback(() => {
    // 收起消息区域内所有展开的 <details> 元素
    if (!scrollRef.current) return;
    const details = scrollRef.current.querySelectorAll('details[open]');
    details.forEach(d => {
      d.removeAttribute('open');
    });
  }, []);
  const abortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);
  const convKeyRef = useRef<string | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (autoScrollTimerRef.current) clearTimeout(autoScrollTimerRef.current);
    };
  }, []);

  useEffect(() => {
    fetch('/api/preferences').then(r => r.json()).then(d => {
      const ps = d?.llm_profiles ?? [];
      setProfiles(ps);
      // 优先用 localStorage 里保存的选择，其次用第一个 profile
      const saved = typeof localStorage !== 'undefined' ? localStorage.getItem('cross-qa-profile-id') : null;
      const exists = saved && ps.some((p: any) => p.id === saved);
      if (exists) {
        setProfileId(saved!);
      } else if (ps.length > 0 && !profileId) {
        setProfileId(ps[0].id);
      }
    }).catch(() => {});
  }, []);

  // 流式回复时跟随到底部；用户主动向上滚动则暂停跟随，
  // 回到底部附近后自动恢复跟随。
  const scrollToBottom = useCallback(() => {
    if (!followStreamRef.current) return;
    const el = scrollRef.current;
    if (!el) return;
    autoScrollingRef.current = true;
    if (autoScrollTimerRef.current) clearTimeout(autoScrollTimerRef.current);
    autoScrollTimerRef.current = setTimeout(() => {
      autoScrollingRef.current = false;
      autoScrollTimerRef.current = null;
    }, 400);
    // 等到下一帧（并且再等一帧）让新消息高度完成渲染后再贴底，
    // 避免 setTimeout 读到旧 scrollHeight 导致滚动停留在旧高度。
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        if (!scrollRef.current) return;
        scrollRef.current.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' });
      });
    });
  }, []);

  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const prevTop = lastScrollTopRef.current;
    lastScrollTopRef.current = el.scrollTop;
    const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;

    // 程序性滚动（smooth 跟随）进行中不改变用户跟随状态。
    if (autoScrollingRef.current) {
      if (distFromBottom < 24) {
        // 已到达底部，跟随恢复
        followStreamRef.current = true;
        autoScrollingRef.current = false;
        lastScrollTopRef.current = el.scrollTop;
      }
      return;
    }

    if (distFromBottom < 24) {
      // 已经回到底部附近，恢复跟随
      followStreamRef.current = true;
      return;
    }
    const isScrollingUp = el.scrollTop < prevTop - 2;
    if (isScrollingUp && distFromBottom > 48) {
      // 明确向上滚动，暂停跟随，避免用户翻看历史时被拉回底部
      followStreamRef.current = false;
      return;
    }
    // 向下滚动且接近底部时保持跟随
    if (distFromBottom < 120) {
      followStreamRef.current = true;
    }
  }, []);

  const askQuestion = useCallback(async (question: string) => {
    if (!question.trim() || loading) return;
    const q = question.trim();
    // 在首轮提问前就确定会话 key，保证 memory-search 的原文候选能按会话持久化到后端。
    if (!convKeyRef.current) {
      convKeyRef.current = `cross-qa:${Date.now()}`;
      setConversationKey(convKeyRef.current);
    }
    setMessages(prev => [...prev, { role: 'user', content: q }]);
    setLoading(true);
    scrollToBottom();

    const startTime = Date.now();

    try {
      // ── Step 1: 记忆优先两级检索 ──
      // 调 /api/ai/memory-search，后端用 LLM 分解问题（needs_memory gate +
      // 实体/概念/时间提取），然后搜索 mem_facts 并提取源聊天记录
      setMessages(prev => [...prev, { role: 'system', content: '正在检索记忆库...', searching: true }]);
      scrollToBottom();

      // 提取上一轮分解结果，用于连续问答时沿用实体/概念/时间
      const lastAssistantWithMem = [...messages].reverse().find(m =>
        m.role === 'assistant' && m.memorySearchData?.decomposition
      );
      const prevDecomp = lastAssistantWithMem?.memorySearchData?.decomposition || null;

      // SSE 流式读取 memory-search，实时显示每个步骤的进度
      const memResp = await fetch('/api/ai/memory-search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query: q,
          profile_id: profileId,
          conversation_key: convKeyRef.current ?? '',
          previous_decomposition: prevDecomp,
        }),
      });
      if (!memResp.ok) {
        const errText = await memResp.text().catch(() => memResp.statusText);
        throw new Error(`记忆检索失败 (${memResp.status})：${errText.slice(0, 200)}`);
      }

      const memReader = memResp.body?.getReader();
      if (!memReader) throw new Error('无法读取记忆检索响应');
      const memDecoder = new TextDecoder();
      let memBuf = '';
      let memData: MemorySearchResponse | null = null;

      while (true) {
        const { done, value } = await memReader.read();
        if (done) break;
        memBuf += memDecoder.decode(value, { stream: true });
        const memLines = memBuf.split('\n');
        memBuf = memLines.pop() ?? '';
        for (const line of memLines) {
          if (!line.startsWith('data: ')) continue;
          try {
            const evt = JSON.parse(line.slice(6)) as {
              type: 'progress' | 'result' | 'done';
              step?: string;
              detail?: string;
              data?: MemorySearchResponse;
            };
            if (evt.type === 'progress' && evt.detail) {
              setMessages(prev => {
                const next = [...prev];
                const last = next[next.length - 1];
                if (last?.searching) {
                  const step: ProgressStep = {
                    step: evt.step || '',
                    detail: evt.detail!,
                    timestamp: Date.now(),
                  };
                  const steps = last.progressSteps ? [...last.progressSteps, step] : [step];
                  next[next.length - 1] = { ...last, content: evt.detail!, progressSteps: steps };
                }
                return next;
              });
              scrollToBottom();
            }
            if (evt.type === 'result' && evt.data) {
              memData = evt.data;
              // 尽早设置 memorySearchData，让检索详情在后续步骤中立即可见
              setMessages(prev => {
                const next = [...prev];
                if (next[next.length - 1]?.searching) {
                  next[next.length - 1] = { ...next[next.length - 1], memorySearchData: memData || undefined };
                }
                return next;
              });
            }
          } catch {
            continue;
          }
        }
      }

      if (!memData) {
        throw new Error('记忆检索未返回结果');
      }

      // 收集 memory-search 消耗的 token
      let totalTokens = memData.token_usage?.total_tokens ?? 0;

      // ── Step 2: 构建 dataContext ──
      let dataContext = '';

      // 即使不需要检索记忆（追问/总结类），也注入置顶的背景知识
      if (memData.pinned_facts?.length > 0) {
        dataContext += '\n【手工置顶的背景知识】\n';
        for (const pf of memData.pinned_facts) {
          dataContext += `- ${pf.fact}\n`;
        }
      }

      if (memData.decomposition?.needs_memory === false) {
        // 追问/总结类问题，可直接从上下文回答，不需要检索
        // 置顶事实已注入，dataContext 不再添加其他内容
      } else {
        // 从源聊天记录构建 context
        if (memData.sources?.length > 0) {
          dataContext += '\n【从记忆库检索到的相关聊天记录】\n';
          for (const src of memData.sources) {
            const factText = src.fact?.fact || '';
            const sourceName = src.source_name || src.fact?.contact_key || '未知';
            dataContext += `\n■ ${privacyMode ? '***' : sourceName}（${factText}）\n`;
            // 用 [说话人]: 文本 格式，每条一行，紧凑不浪费空间
            const lines = (src.messages || []).map(msg => {
              const senderLabel = privacyMode ? '***' : (msg.sender || '未知');
              return `[${senderLabel}]: ${truncateMsgContent(msg.content)}`;
            });
            dataContext += lines.join('\n') + '\n';
          }
        }
        const vecMsgs = memData.vec_messages;
        if (vecMsgs && vecMsgs.length > 0) {
          dataContext += '\n【从原始消息向量检索到的相关聊天记录】\n';
          for (const vm of vecMsgs) {
            const senderLabel = privacyMode ? '***' : (vm.sender || '未知');
            dataContext += `[${vm.datetime} ${senderLabel}]: ${truncateMsgContent(vm.content)}\n`;
          }
        }
        if (memData.raw_hits && memData.raw_hits.length > 0) {
          dataContext += '\n【原文精确命中（聊天记录原文）】\n';
          for (const rh of memData.raw_hits) {
            const senderLabel = privacyMode ? '***' : (rh.sender || '未知');
            const sourceLabel = privacyMode ? '***' : (rh.source_name || '未知');
            dataContext += `[${sourceLabel} ${rh.datetime} ${senderLabel}]: ${truncateMsgContent(rh.content)}\n`;
          }
        }
        if (!dataContext.trim()) {
          dataContext = '【未找到相关记忆】';
        }
      }

      // ── Step 3: LLM 汇总回答 ──
      setMessages(prev => {
        const next = [...prev];
        next[next.length - 1] = { role: 'system', content: '正在生成回答...', searching: true };
        return next;
      });
      scrollToBottom();

      // 收集之前的对话历史（用户提问 + AI 回答）
      const history = messages.filter(m =>
        (m.role === 'user' || m.role === 'assistant') && !m.searching && m.content
      ).map(m => ({ role: m.role, content: m.content }));

      abortRef.current = new AbortController();
      const llmMessages: LLMMessage[] = [
        { role: 'system', content: `你是 WeLink 的 AI 助手，用户刚问了一个关于微信聊天记录的问题。
以下是从数据库中检索到的相关数据。请基于这些数据回答用户的问题。

要求：
1. 用中文回答，简洁清晰
2. 直接回答问题，不要废话
3. 如果数据不足以回答，诚实说明
4. 用 Markdown 格式排版（列表、粗体等）
5. 如果涉及多个联系人，用列表列出并简要说明
6. 每段故事、结论或场景都要说明其依据的聊天记录原文（含前后上下文）作为佐证；引用原文时至少保留该事件前后各 5 条上下文聊天信息；若前后各 5 条仍不足以完整表达一个事件或观点，则继续延伸，直到能完整表达该事件为止。` },
        ...history,
        { role: 'user', content: `问题：${q}\n\n${dataContext}` },
      ];
      const resp = await fetch('/api/ai/analyze', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username: '__cross_contact__',
          is_group: false,
          messages: llmMessages,
          profile_id: profileId,
          skip_memory: true,
          query: q,
          conversation_key: convKeyRef.current ?? '',
        }),
        signal: abortRef.current.signal,
      });
      if (!resp.ok) {
        throw new Error(`AI 接口返回错误 ${resp.status}（${resp.statusText}），请稍后重试`);
      }

      const reader = resp.body?.getReader();
      if (!reader) throw new Error('无法读取响应');
      const decoder = new TextDecoder();
      let buf = '';
      let full = '';

      // 替换 searching 消息为正式回答，附带（精简后的）检索详情
      setMessages(prev => {
        const next = [...prev];
        next[next.length - 1] = { role: 'assistant', content: '', memorySearchData: compactMemorySearchData(memData), llmPrompt: llmMessages };
        return next;
      });

      let streamError: string | null = null;
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        const lines = buf.split('\n');
        buf = lines.pop() ?? '';
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue;
          try {
            const chunk = JSON.parse(line.slice(6)) as { delta?: string; thinking?: string; done?: boolean; error?: string; usage?: StreamUsage };
            if (chunk.error) {
              streamError = chunk.error;
              break;
            }
            if (chunk.usage) {
              totalTokens += chunk.usage.total_tokens ?? 0;
            }
            if (chunk.thinking) {
              setMessages(prev => {
                const next = [...prev];
                const last = next[next.length - 1];
                if (last) {
                  next[next.length - 1] = { ...last, thinking: (last.thinking || '') + chunk.thinking };
                }
                return next;
              });
              scrollToBottom();
            }
            if (chunk.delta) {
              full += chunk.delta;
              setMessages(prev => {
                const next = [...prev];
                next[next.length - 1] = { ...next[next.length - 1], content: full };
                return next;
              });
              scrollToBottom();
            }
          } catch {
            // 单条 SSE 解析失败时跳过，不中断整个流
            continue;
          }
        }
        if (streamError) break;
      }
      // 如果流出错，追加明确的错误提示
      if (streamError) {
        const errorHint = full.trim()
          ? `\n\n---\n⚠️ AI 回复中断：${streamError}\n可以在下方继续提问重试。`
          : `⚠️ AI 回复失败：${streamError}\n可以在下方继续提问重试。`;
        full += errorHint;
        setMessages(prev => {
          const next = [...prev];
          next[next.length - 1] = { ...next[next.length - 1], content: full };
          return next;
        });
        scrollToBottom();
      }
      // 如果组件已卸载（用户离开了页面），直接保存最终结果到后端
      if (!mountedRef.current) {
        const key = convKeyRef.current || `cross-qa:${Date.now()}`;
        const finalMessages = [
          ...messages.filter(m => (m.role === 'user' || m.role === 'assistant') && !m.searching && m.content),
          { role: 'assistant' as const, content: full, memorySearchData: memData, llmPrompt: llmMessages,
            tokenUsage: { prompt_tokens: 0, output_tokens: 0, total_tokens: totalTokens },
            elapsedMs: Date.now() - startTime },
        ];
        fetch('/api/ai/conversations', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ key, messages: finalMessages }),
        }).then(() => {
          window.dispatchEvent(new Event('welink:conversation-saved'));
        }).catch(() => {});
        return;
      }
      // 组件仍挂载：保存 token 使用统计和耗时，并把检索详情压缩到最轻状态，
      // 释放内存（llmPrompt / rerank 明细等纯调试数据不再留在 React 状态里）。
      setMessages(prev => {
        const next = [...prev];
        const last = next[next.length - 1];
        if (last?.role === 'assistant') {
          next[next.length - 1] = {
            ...last,
            memorySearchData: compactMemorySearchData(last.memorySearchData as MemorySearchResponse),
            tokenUsage: { prompt_tokens: 0, output_tokens: 0, total_tokens: totalTokens },
            elapsedMs: Date.now() - startTime,
          };
        }
        return next;
      });
    } catch (e: unknown) {
      if ((e as Error).name !== 'AbortError') {
        setMessages(prev => {
          const next = [...prev];
          if (next[next.length - 1]?.searching) {
            next[next.length - 1] = { role: 'assistant', content: `出错了：${(e as Error).message || '未知错误'}` };
          } else {
            next.push({ role: 'assistant', content: `出错了：${(e as Error).message || '未知错误'}` });
          }
          return next;
        });
      }
    } finally {
      setLoading(false);
      abortRef.current = null;
    }
  }, [loading, profileId, privacyMode, scrollToBottom]);

  // 自动保存对话（包括 AI 正在回答时）
  useEffect(() => {
    if (messages.length === 0) return;

    let key = conversationKey;
    if (!key) {
      key = `cross-qa:${Date.now()}`;
      setConversationKey(key);
    }
    convKeyRef.current = key;

    const saveData = messages
      // 只保存有明确内容的 user/assistant 消息。searching 中间态只用于
      // 当前会话内展示，跨进程恢复时不应再以“正在生成/检索”的假状态出现。
      .filter(m => m.content && (m.role === 'user' || m.role === 'assistant'))
      .map(m => {
        const item: any = { role: m.role, content: m.content };
        if (m.searching) item.searching = true;
        if (m.memorySearchData) item.memorySearchData = m.memorySearchData;
        if (m.llmPrompt) item.llmPrompt = m.llmPrompt;
        return item;
      });
    // 至少要有一条 user 消息才保存
    if (!saveData.some(m => m.role === 'user')) return;
    fetch('/api/ai/conversations', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, messages: saveData }),
    }).then(() => {
      window.dispatchEvent(new Event('welink:conversation-saved'));
    }).catch(() => {});
  }, [messages, conversationKey]);

  const loadConversation = useCallback(async (key: string) => {
    try {
      const resp = await fetch(`/api/ai/conversations?key=${encodeURIComponent(key)}`);
      const data = await resp.json();
      if (data.messages?.length) {
        // 恢复历史时丢掉“正在检索/生成”且无内容的残留中间态，
        // 并把任何带内容的 searching 消息降级为普通 assistant 消息，
        // 避免容器重启后出现永不结束的转圈气泡。
        const restored = (data.messages as any[])
          .filter(m => !!String(m.content ?? '').trim())
          .map((m: any) => ({
            role: m.role as 'user' | 'assistant' | 'system',
            content: m.content,
            searching: false,
            memorySearchData: compactMemorySearchData(m.memorySearchData),
            llmPrompt: m.llmPrompt,
          }));
        if (restored.length === 0) return;
        setMessages(restored);
        setConversationKey(key);
        convKeyRef.current = key;
      }
    } catch {}
  }, []);

  const startNew = useCallback(() => {
    const oldKey = convKeyRef.current;
    convKeyRef.current = null;
    setMessages([]);
    setConversationKey(null);
    if (oldKey) {
      // 清空后端按会话保存的原文候选，避免孤儿数据堆积。
      fetch(`/api/ai/conversations/candidates?key=${encodeURIComponent(oldKey)}`, { method: 'DELETE' }).catch(() => {});
    }
  }, []);

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 rounded-xl bg-gradient-to-br from-[#576b95] to-[#10aeff] flex items-center justify-center">
            <Globe size={16} className="text-white" />
          </div>
          <div>
            <h3 className="text-sm font-bold dk-text">跨联系人问答</h3>
            <p className="text-[10px] text-gray-400">AI 自动搜索所有聊天记录回答你的问题</p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          {profiles.length > 1 && (
            <select
              value={profileId}
              onChange={e => {
                setProfileId(e.target.value);
                try { localStorage.setItem('cross-qa-profile-id', e.target.value); } catch {}
              }}
              className="text-[10px] text-[#576b95] bg-[#576b95]/10 px-2 py-0.5 rounded-full font-semibold border-0 outline-none cursor-pointer"
            >
              {profiles.map(p => (
                <option key={p.id} value={p.id}>{p.provider}{p.model ? ` · ${p.model}` : ''}</option>
              ))}
            </select>
          )}
          {messages.length > 0 && (
            <button onClick={startNew} className="text-gray-400 hover:text-red-400 p-1 transition-colors" title="清空对话">
              <Trash2 size={14} />
            </button>
          )}
        </div>
      </div>

      {/* 历史记录 */}
      <ConversationHistory
        prefix="cross-qa:"
        currentKey={conversationKey}
        onSelect={loadConversation}
        onNew={startNew}
        className="mb-3"
      />

      {/* Messages */}
      <div ref={scrollRef} onScroll={handleScroll} className="flex-1 overflow-y-auto space-y-3 min-h-0 mb-3">
        {messages.length === 0 && (
          <div className="flex flex-col items-center justify-center h-full gap-4 text-center">
            <Globe size={40} className="text-gray-200" />
            <p className="text-sm text-gray-400">问我任何关于聊天记录的问题</p>
            <div className="flex flex-wrap justify-center gap-2 max-w-md">
              {EXAMPLE_QUESTIONS.map(q => (
                <button
                  key={q}
                  onClick={() => { setInput(''); askQuestion(q); }}
                  disabled={loading}
                  className="px-3 py-1.5 rounded-full text-xs bg-gray-100 dark:bg-white/5 text-gray-500 hover:bg-[#e7f8f0] hover:text-[#07c160] transition-colors"
                >
                  {q}
                </button>
              ))}
            </div>
          </div>
        )}

        {(() => {
          // Group messages into Q&A pairs (user question + following messages until next user msg)
          const groups: number[][] = [];
          let cur: number[] | null = null;
          messages.forEach((m, i) => {
            if (m.role === 'user') {
              if (cur) groups.push(cur);
              cur = [i];
            } else {
              if (!cur) { cur = [i]; }
              else { cur.push(i); }
            }
          });
          if (cur) groups.push(cur);
          return groups.map((indices, gi) => (
            <div key={gi} data-qa-pair={gi} className="space-y-3">
              {indices.map(i => {
                const msg = messages[i];
                return (
          <div key={i} className={`flex gap-2.5 ${msg.role === 'user' ? 'flex-row-reverse' : ''}`}>
            {msg.role !== 'user' && (
              <div className={`w-7 h-7 rounded-full flex-shrink-0 flex items-center justify-center text-white text-xs ${
                msg.searching ? 'bg-[#ff9500]' : 'bg-[#576b95]'
              }`}>
                {msg.searching ? (msg.tool === 'search' ? <Search size={12} /> : msg.tool === 'calendar' ? <Calendar size={12} /> : <Bot size={12} />) : <Bot size={12} />}
              </div>
            )}
            <div className={`max-w-[80%] ${msg.role === 'user' ? 'items-end' : 'items-start'} flex flex-col`}>
              <div className={`px-3 py-2 rounded-2xl text-sm leading-relaxed ${
                msg.role === 'user'
                  ? 'bg-[#07c160] text-white rounded-br-sm'
                  : msg.searching
                    ? 'bg-orange-50 dark:bg-orange-900/20 text-orange-600 dark:text-orange-300 text-xs italic'
                    : 'bg-[#f0f0f0] dark:bg-white/10 rounded-bl-sm'
              }`}>
                {msg.role === 'assistant' && !msg.searching ? (
                  msg.content ? (
                    <div data-msg-idx={i} className="prose prose-sm dark:prose-invert max-w-none prose-strong:text-[#07c160]">
                      <ReactMarkdown remarkPlugins={[remarkGfm]}>{preserveMarkdownBlockquote(msg.content)}</ReactMarkdown>
                    </div>
                  ) : (
                    <span className="flex items-center gap-1.5 text-xs text-gray-400">
                      <Loader2 size={12} className="animate-spin" />
                      正在生成回答…
                    </span>
                  )
                ) : msg.searching ? (
                  <div className="space-y-1.5">
                    <span className="flex items-center gap-1.5">
                      <Loader2 size={12} className="animate-spin" />
                      {msg.content}
                    </span>
                    {msg.progressSteps && msg.progressSteps.length >= 1 && (
                      <ProgressTicker steps={msg.progressSteps} />
                    )}
                  </div>
                ) : (
                  msg.content
                )}
              </div>
              {/* 复制 + 分享 */}
              {msg.role === 'assistant' && !msg.searching && msg.content && (
                <div className="flex items-center gap-3 mt-1.5 self-start">
                  <button
                    onClick={() => {
                      navigator.clipboard.writeText(msg.content).then(() => {
                        setCopiedIdx(i);
                        setTimeout(() => setCopiedIdx(-1), 2000);
                      });
                    }}
                    className="flex items-center gap-1 text-xs text-gray-400 hover:text-[#07c160] transition-colors"
                  >
                    {copiedIdx === i ? <Check size={12} className="text-[#07c160]" /> : <Copy size={12} />}
                    {copiedIdx === i ? '已复制' : '复制'}
                  </button>
                  <button
                    onClick={async () => {
                      if (shotLoadingIdx >= 0) return;
                      setShotLoadingIdx(i);
                      try {
                        const userMsg = messages.slice(0, i).reverse().find(m => m.role === 'user');
                        const curProfile = profiles.find(p => p.id === profileId);
                        const result = await generateAIScreenshot({
                          question: userMsg?.content ?? '跨联系人问答',
                          answer: msg.content,
                          isCrossContact: true,
                          stats: {
                            provider: curProfile?.provider,
                            model: curProfile?.model,
                            timestamp: Date.now(),
                          },
                        });
                        if (result.ok) {
                          setShotDoneIdx(i);
                          setTimeout(() => setShotDoneIdx(-1), 2000);
                        }
                      } catch (e) { console.error(e); }
                      finally { setShotLoadingIdx(-1); }
                    }}
                    disabled={shotLoadingIdx >= 0}
                    className="flex items-center gap-1 text-xs text-gray-400 hover:text-[#07c160] transition-colors"
                  >
                    {shotLoadingIdx === i ? <Loader2 size={12} className="animate-spin" /> : shotDoneIdx === i ? <Check size={12} className="text-[#07c160]" /> : <Camera size={12} />}
                    {shotLoadingIdx === i ? '截图中…' : shotDoneIdx === i ? '已复制' : '截图'}
                  </button>
                  {msg.tokenUsage && (
                    <span className="text-xs text-gray-400 flex items-center gap-1">
                      <span className="opacity-60">⚡</span>
                      {formatTokens(msg.tokenUsage.total_tokens)} tokens
                      {msg.elapsedMs ? (
                        <span className="ml-1 opacity-60">· {(msg.elapsedMs / 1000).toFixed(1)}s</span>
                      ) : null}
                    </span>
                  )}
                </div>
              )}
              {(msg.role === "assistant") && !msg.searching && msg.content && msg.memorySearchData && (
                <RetrievalDetails
                  msg={msg}
                  privacyMode={privacyMode}
                  conversationKey={convKeyRef.current ?? undefined}
                  onAskRaw={(question) => { setInput(question); void askQuestion(question); }}
                />
              )}
              {/* 搜索结果完整列表 */}
              {msg.searchHits && msg.searchHits.length > 0 && !msg.searching && msg.content && (
                <details className="mt-2 w-full">
                  <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-[#07c160] transition-colors select-none">
                    查看全部 {msg.searchHits.length} 个匹配（点击展开）
                  </summary>
                  <div className="mt-1.5 max-h-48 overflow-y-auto space-y-1 bg-white dark:bg-gray-900 rounded-xl p-2 border border-gray-100 dark:border-gray-800">
                    {msg.searchHits
                      .sort((a, b) => b.count - a.count)
                      .map(hit => (
                        <button
                          key={hit.username}
                          onClick={() => setPopupHit(hit)}
                          className="flex items-center justify-between text-xs px-2 py-1.5 rounded-lg hover:bg-[#e7f8f0] dark:hover:bg-[#07c160]/10 w-full text-left transition-colors cursor-pointer"
                        >
                          <span className={`font-medium text-[#1d1d1f] dk-text hover:text-[#07c160] ${privacyMode ? 'privacy-blur' : ''}`}>
                            {hit.is_group ? '🏠 ' : ''}{hit.display_name}
                          </span>
                          <span className="text-gray-400 flex-shrink-0 ml-2">{hit.count} 条 →</span>
                        </button>
                      ))
                    }
                  </div>
                </details>
              )}
            </div>
          </div>
                );
              })}
            </div>
          ));
        })()}
      </div>

      {/* 收起全部 + Input */}
      <div className="flex items-center justify-between mb-1.5">
        <button
          onClick={collapseAllDetails}
          className="text-[10px] text-gray-400 hover:text-[#07c160] transition-colors"
        >
          收起全部
        </button>
      </div>
      {/* Input */}
      <form onSubmit={e => { e.preventDefault(); const q = input.trim(); if (q) { setInput(''); askQuestion(q); } }} className="flex gap-2">
        <input
          type="text"
          value={input}
          onChange={e => setInput(e.target.value)}
          placeholder="问我关于聊天记录的问题..."
          className="flex-1 px-4 py-2.5 rounded-2xl border border-gray-200 dark:border-gray-700 text-sm bg-white dark:bg-gray-800 dk-input focus:outline-none focus:border-[#07c160]"
          disabled={loading}
        />
        <button
          type="submit"
          disabled={!input.trim() || loading || !profileId}
          className="px-4 py-2.5 bg-[#576b95] text-white rounded-2xl text-sm font-bold disabled:opacity-40 hover:bg-[#4a5d82] transition-colors flex-shrink-0"
        >
          <Send size={16} />
        </button>
      </form>

      {onOpenSettings && !profileId && (
        <p className="text-[10px] text-gray-400 mt-2">
          需要先在 <button onClick={onOpenSettings} className="text-[#07c160] underline">设置</button> 中配置 AI 接口
        </p>
      )}

      {/* 匹配结果弹窗：展示聊天记录原文 */}
      {popupHit && (
        <div className="fixed inset-0 z-[9000] flex items-center justify-center bg-black/40" onClick={() => setPopupHit(null)}>
          <div className="w-[500px] max-h-[70vh] bg-white dark:bg-[#1d1d1f] rounded-2xl shadow-2xl flex flex-col overflow-hidden" onClick={e => e.stopPropagation()}>
            {/* Header */}
            <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100 dark:border-white/10">
              <div className="flex items-center gap-2 min-w-0">
                <span className={`font-semibold text-sm truncate ${privacyMode ? 'privacy-blur' : ''}`}>
                  {popupHit.is_group ? '🏠 ' : ''}{popupHit.display_name}
                </span>
                <span className="text-xs text-gray-400 flex-shrink-0">{popupHit.count} 条匹配</span>
              </div>
              <button onClick={() => setPopupHit(null)} className="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 flex-shrink-0">
                <X size={16} />
              </button>
            </div>
            {/* Messages */}
            <div className="flex-1 overflow-y-auto p-3 space-y-2">
              {popupHit.messages?.map((msg, idx) => (
                <div key={idx} className={`flex ${msg.is_mine ? 'flex-row-reverse' : 'flex-row'} gap-2`}>
                  <div className={`max-w-[75%] px-3 py-2 rounded-2xl text-sm ${msg.is_mine ? 'bg-[#07c160] text-white rounded-br-sm' : 'bg-[#f0f0f0] dark:bg-white/10 rounded-bl-sm'}`}>
                    <div className="text-[10px] text-gray-400 mb-0.5">{msg.date} {msg.time}</div>
                    <div className="whitespace-pre-wrap break-words">{msg.content}</div>
                  </div>
                </div>
              ))}
              {(!popupHit.messages || popupHit.messages.length === 0) && (
                <p className="text-center text-sm text-gray-400 py-8">无匹配消息</p>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
