/**
 * 跨联系人 AI 问答 — Agent 模式：LLM 解析意图 → 自动搜索/查询 → LLM 汇总回答
 * 支持问题如："谁跟我聊过旅行""去年国庆和谁聊天了""哪些朋友经常提到加班"
 */

import React, { useState, useRef, useCallback, useEffect, useMemo } from 'react';
import { Globe, Send, Loader2, Trash2, Bot, Search, Calendar, RotateCcw, Check, Copy, Camera, X } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { generateAIScreenshot } from '../../utils/shareImage';
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

interface MemorySearchResponse {
  decomposition: QueryDecomposition;
  resolved_entities: ResolvedEntity[];
  facts: MemFact[];
  sources: FactSource[];
  pinned_facts: MemFact[];
  token_usage?: StreamUsage;
  decompose_prompt?: LLMMessage[];
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
}

function formatTokens(n: number): string {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
  return n.toString();
}

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

  const collapseAllDetails = useCallback(() => {
    // 收起消息区域内所有展开的 <details> 元素
    if (!scrollRef.current) return;
    const details = scrollRef.current.querySelectorAll('details[open]');
    details.forEach(d => {
      d.removeAttribute('open');
    });
  }, []);
  const abortRef = useRef<AbortController | null>(null);

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

  const scrollToBottom = useCallback(() => {
    setTimeout(() => scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' }), 50);
  }, []);

  const askQuestion = useCallback(async (question: string) => {
    if (!question.trim() || loading) return;
    const q = question.trim();
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

      const memResp = await fetch('/api/ai/memory-search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query: q,
          profile_id: profileId,
        }),
      });
      const memData = await memResp.json() as MemorySearchResponse;

      // 收集 memory-search 消耗的 token
      let totalTokens = memData.token_usage?.total_tokens ?? 0;

      // ── Step 2: 构建 dataContext ──
      let dataContext = '';

      if (memData.decomposition?.needs_memory === false) {
        // 追问/总结类问题，可直接从上下文回答，不需要检索
        dataContext = '';
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
              return `[${senderLabel}]: ${msg.content}`;
            });
            dataContext += lines.join('\n') + '\n';
          }
        }
        // 添加置顶事实
        if (memData.pinned_facts?.length > 0) {
          dataContext += '\n【手工置顶的背景知识】\n';
          for (const pf of memData.pinned_facts) {
            dataContext += `- ${pf.fact}\n`;
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
5. 如果涉及多个联系人，用列表列出并简要说明` },
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

      // 替换 searching 消息为正式回答，附带检索详情
      setMessages(prev => {
        const next = [...prev];
        next[next.length - 1] = { role: 'assistant', content: '', memorySearchData: memData, llmPrompt: llmMessages };
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
            const chunk = JSON.parse(line.slice(6)) as { delta?: string; done?: boolean; error?: string; usage?: StreamUsage };
            if (chunk.error) {
              streamError = chunk.error;
              break;
            }
            if (chunk.usage) {
              totalTokens += chunk.usage.total_tokens ?? 0;
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
      // 保存 token 使用统计和耗时到最后一条 assistant 消息
      setMessages(prev => {
        const next = [...prev];
        if (next[next.length - 1]?.role === 'assistant') {
          next[next.length - 1] = { ...next[next.length - 1], tokenUsage: { prompt_tokens: 0, output_tokens: 0, total_tokens: totalTokens }, elapsedMs: Date.now() - startTime };
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

  // 自动保存对话
  useEffect(() => {
    if (loading || messages.length === 0) return;
    const hasAssistant = messages.some(m => m.role === 'assistant' && !m.searching && m.content);
    if (!hasAssistant) return;

    let key = conversationKey;
    if (!key) {
      key = `cross-qa:${Date.now()}`;
      setConversationKey(key);
    }

    const saveData = messages
      .filter(m => m.role === 'user' || (m.role === 'assistant' && !m.searching))
      .map(m => {
        const item: any = { role: m.role, content: m.content };
        if (m.memorySearchData) item.memorySearchData = m.memorySearchData;
        if (m.llmPrompt) item.llmPrompt = m.llmPrompt;
        return item;
      });
    fetch('/api/ai/conversations', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, messages: saveData }),
    }).then(() => {
      window.dispatchEvent(new Event('welink:conversation-saved'));
    }).catch(() => {});
  }, [messages, loading, conversationKey]);

  const loadConversation = useCallback(async (key: string) => {
    try {
      const resp = await fetch(`/api/ai/conversations?key=${encodeURIComponent(key)}`);
      const data = await resp.json();
      if (data.messages?.length) {
        setMessages(data.messages.map((m: any) => ({
          role: m.role as 'user' | 'assistant' | 'system',
          content: m.content,
          memorySearchData: m.memorySearchData,
          llmPrompt: m.llmPrompt,
        })));
        setConversationKey(key);
      }
    } catch {}
  }, []);

  const startNew = useCallback(() => {
    setMessages([]);
    setConversationKey(null);
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
      <div ref={scrollRef} className="flex-1 overflow-y-auto space-y-3 min-h-0 mb-3">
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
                  <div data-msg-idx={i} className="prose prose-sm dark:prose-invert max-w-none prose-strong:text-[#07c160]">
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.content || '...'}</ReactMarkdown>
                  </div>
                ) : msg.searching ? (
                  <span className="flex items-center gap-1.5">
                    <Loader2 size={12} className="animate-spin" />
                    {msg.content}
                  </span>
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
              {/* 检索详情下拉框 */}
              {msg.role === 'assistant' && !msg.searching && msg.content && (msg.memorySearchData || msg.llmPrompt) && (
                <details className="mt-1.5 w-full">
                  <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-[#07c160] transition-colors select-none flex items-center gap-1">
                    <Search size={10} />
                    检索详情
                  </summary>
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
                    {/* 嵌套下拉框：查询分解 prompt */}
                    {msg.memorySearchData?.decompose_prompt && msg.memorySearchData.decompose_prompt.length > 0 && (
                      <details className="mt-2">
                        <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-[#07c160] transition-colors select-none">
                          查询分解 prompt（{msg.memorySearchData.decompose_prompt.length} 条消息）
                        </summary>
                        <div className="mt-2 space-y-2">
                          {msg.memorySearchData.decompose_prompt.map((m, idx) => (
                            <div key={idx} className="p-2 bg-white dark:bg-gray-900 rounded-lg border border-gray-100 dark:border-gray-800">
                              <div className="text-[10px] font-semibold text-gray-400 mb-1">{m.role}</div>
                              <div className="text-xs text-gray-600 dark:text-gray-300 whitespace-pre-wrap break-words">{m.content}</div>
                            </div>
                          ))}
                        </div>
                      </details>
                    )}
                    {/* 嵌套下拉框：发给 LLM 的原始 prompt */}
                    {msg.llmPrompt && msg.llmPrompt.length > 0 && (
                      <details className="mt-2">
                        <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-[#07c160] transition-colors select-none">
                          发送给 LLM 的原始 prompt（{msg.llmPrompt.length} 条消息）
                        </summary>
                        <div className="mt-2 space-y-2">
                          {msg.llmPrompt.map((m, idx) => (
                            <div key={idx} className="p-2 bg-white dark:bg-gray-900 rounded-lg border border-gray-100 dark:border-gray-800">
                              <div className="text-[10px] font-semibold text-gray-400 mb-1">{m.role}</div>
                              <div className="text-xs text-gray-600 dark:text-gray-300 prose prose-sm dark:prose-invert max-w-none">
                                <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.content}</ReactMarkdown>
                              </div>
                            </div>
                          ))}
                        </div>
                      </details>
                    )}
                  </div>
                </details>
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
