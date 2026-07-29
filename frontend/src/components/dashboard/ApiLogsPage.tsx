/**
 * API 日志页面 — 展示前端 fetch + 后端 LLM API 调用的合并日志
 * 用于调试 "Unexpected token '<'" 等后端返回非 JSON 的问题
 */

import React, { useState, useEffect, useCallback } from 'react';
import { Trash2, ChevronDown, AlertCircle, Info, AlertTriangle, Search, Cloud, Server } from 'lucide-react';
import {
  type ApiLogEntry,
  type LogLevel,
  getEntries,
  clearEntries,
  subscribe,
} from '../../utils/apiLogger';

// 后端 LLM API 调用日志条目
interface LLMApiLogEntry {
  id: number;
  timestamp: string;
  method: string;
  url: string;
  provider: string;
  model: string;
  feature: string;
  request_body: string;
  status: number;
  response_body: string;
  duration_ms: number;
  error: string;
}

// 合并后的统一日志条目
interface UnifiedLogEntry {
  id: string;
  source: 'frontend' | 'backend';
  timestamp: string;
  level: LogLevel;
  method: string;
  url: string;
  status: number | null;
  statusText: string;
  durationMs: number;
  requestSnippet: string;
  responseSnippet: string;
  error: string;
  nonJsonResponse: boolean;
  provider?: string;
  model?: string;
  feature?: string;
}

const LEVEL_CONFIG: Record<LogLevel, { icon: React.ReactNode; color: string; bg: string }> = {
  error: { icon: <AlertCircle size={14} />, color: 'text-red-500', bg: 'bg-red-50 dark:bg-red-500/10' },
  warn: { icon: <AlertTriangle size={14} />, color: 'text-amber-500', bg: 'bg-amber-50 dark:bg-amber-500/10' },
  info: { icon: <Info size={14} />, color: 'text-blue-500', bg: 'bg-blue-50 dark:bg-blue-500/10' },
};

function formatTime(iso: string): string {
  const d = new Date(iso);
  const hh = d.getHours().toString().padStart(2, '0');
  const mm = d.getMinutes().toString().padStart(2, '0');
  const ss = d.getSeconds().toString().padStart(2, '0');
  const ms = d.getMilliseconds().toString().padStart(3, '0');
  return `${hh}:${mm}:${ss}.${ms}`;
}

function formatUrl(url: string): string {
  // 去掉前面的 /api 前缀，缩短显示
  return url.replace(/^\/api/, '') || url;
}

// 默认只显示前 500 字符，避免大请求体占用过多内存
const DISPLAY_TRUNCATE = 500;

// 尝试将内容格式化为美观的 JSON；非 JSON 则原样返回
function formatContent(content: string): string {
  const trimmed = content.trim();
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try {
      const parsed = JSON.parse(trimmed);
      // Pretty-print, then convert escaped \n in string values to real newlines
      return JSON.stringify(parsed, null, 2).replace(/\\n/g, '\n');
    } catch {
      // 不是合法 JSON，原样返回
    }
  }
  // 将字面量 \n（反斜杠+n）转换为真正的换行符
  return content.replace(/\\n/g, '\n');
}

// 可折叠的代码片段块：默认截断，点击"查看全部"展开
const SnippetBlock: React.FC<{ label: string; content: string; bg?: string }> = ({ label, content, bg }) => {
  const [showAll, setShowAll] = useState(false);
  const formatted = formatContent(content);
  const isLong = formatted.length > DISPLAY_TRUNCATE;
  const display = (isLong && !showAll) ? formatted.slice(0, DISPLAY_TRUNCATE) + '…[truncated]' : formatted;
  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <span className="text-[10px] text-gray-400 font-bold">{label}</span>
        {isLong && (
          <button
            onClick={() => setShowAll(!showAll)}
            className="text-[10px] text-[#07c160] hover:underline"
          >
            {showAll ? '收起' : '查看全部'}
          </button>
        )}
      </div>
      <pre className={`text-xs text-gray-600 dark:text-gray-300 ${bg ?? 'bg-gray-50 dark:bg-white/5'} rounded p-2 overflow-x-auto whitespace-pre-wrap break-all`}>{display}</pre>
    </div>
  );
};

export const ApiLogsPage: React.FC = () => {
  const [frontendEntries, setFrontendEntries] = useState<ApiLogEntry[]>([]);
  const [backendEntries, setBackendEntries] = useState<LLMApiLogEntry[]>([]);
  const [filter, setFilter] = useState<'all' | 'ai' | 'error' | 'warn' | 'info'>('ai');
  const [search, setSearch] = useState('');
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // 拉取后端 LLM API 日志
  const fetchBackendLogs = useCallback(async () => {
    try {
      const resp = await fetch('/api/ai/llm-logs');
      if (resp.ok) {
        const data = await resp.json();
        setBackendEntries(data.logs || []);
      }
    } catch { /* ignore */ }
  }, []);

  useEffect(() => {
    setFrontendEntries(getEntries());
    const unsub = subscribe(() => setFrontendEntries([...getEntries()]));
    // 定时拉取后端日志（每 3 秒）
    fetchBackendLogs();
    const interval = setInterval(fetchBackendLogs, 3000);
    return () => { unsub(); clearInterval(interval); };
  }, [fetchBackendLogs]);

  const handleClear = useCallback(async () => {
    clearEntries();
    setBackendEntries([]);
    try { await fetch('/api/ai/llm-logs', { method: 'DELETE' }); } catch { /* ignore */ }
  }, []);

  // 合并前后端日志，按时间排序（最新在前）
  const allEntries: UnifiedLogEntry[] = [
    ...frontendEntries.map(e => ({
      id: `fe-${e.id}`,
      source: 'frontend' as const,
      timestamp: e.timestamp,
      level: e.level,
      method: e.method,
      url: e.url,
      status: e.status,
      statusText: e.statusText,
      durationMs: e.durationMs,
      requestSnippet: e.requestSnippet,
      responseSnippet: e.responseSnippet,
      error: e.error,
      nonJsonResponse: e.nonJsonResponse,
    })),
    ...backendEntries.map(e => ({
      id: `be-${e.id}`,
      source: 'backend' as const,
      timestamp: e.timestamp,
      level: (e.error !== '' || e.status >= 400) ? 'error' as LogLevel : 'info' as LogLevel,
      method: e.method,
      url: e.url,
      status: e.status,
      statusText: '',
      durationMs: e.duration_ms,
      requestSnippet: e.request_body,
      responseSnippet: e.response_body,
      error: e.error,
      nonJsonResponse: false,
      provider: e.provider,
      model: e.model,
      feature: e.feature,
    })),
  ].sort((a, b) => b.timestamp.localeCompare(a.timestamp));

  const filtered = allEntries.filter(e => {
    // 'ai' 标签：只看 AI/LLM 相关调用（后端 LLM 请求 + 前端 /ai/ 路径请求）
    if (filter === 'ai') {
      const isAI = e.source === 'backend' || e.url.includes('/ai/');
      if (!isAI) return false;
    } else if (filter !== 'all' && e.level !== filter) return false;
    if (search) {
      const s = search.toLowerCase();
      return e.url.toLowerCase().includes(s) ||
             e.error.toLowerCase().includes(s) ||
             e.responseSnippet.toLowerCase().includes(s) ||
             e.requestSnippet.toLowerCase().includes(s);
    }
    return true;
  });

  const errorCount = allEntries.filter(e => e.level === 'error').length;
  const warnCount = allEntries.filter(e => e.level === 'warn').length;
  const infoCount = allEntries.filter(e => e.level === 'info').length;

  return (
    <div className="max-w-5xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div>
          <h1 className="text-xl font-bold text-gray-800 dark:text-gray-100">API 日志</h1>
          <p className="text-xs text-gray-400 mt-0.5">
            共 {allEntries.length} 条（前端 {frontendEntries.length} · 后端 {backendEntries.length}）· 错误 {errorCount} · 信息 {infoCount}
          </p>
        </div>
        <button
          onClick={handleClear}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs text-gray-500 hover:text-red-500 hover:bg-red-50 dark:hover:bg-red-500/10 transition-colors"
        >
          <Trash2 size={14} />
          清空
        </button>
      </div>

      {/* Filter bar */}
      <div className="flex items-center gap-2 mb-4">
        <div className="flex-1 relative">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="搜索 URL、错误信息、请求/响应内容..."
            className="w-full pl-9 pr-3 py-2 rounded-lg border border-gray-200 dark:border-gray-700 text-sm bg-white dark:bg-gray-800 dk-input focus:outline-none focus:border-[#07c160]"
          />
        </div>
        <div className="flex items-center gap-1">
          {(['all', 'ai', 'error', 'warn', 'info'] as const).map(f => (
            <button
              key={f}
              onClick={() => setFilter(f)}
              className={`px-2.5 py-1.5 rounded-lg text-xs font-medium transition-colors ${
                filter === f
                  ? 'bg-[#07c160] text-white'
                  : 'text-gray-500 hover:bg-gray-100 dark:hover:bg-white/10'
              }`}
            >
              {f === 'all' ? '全部' : f === 'ai' ? 'AI/LLM' : f === 'error' ? '错误' : f === 'warn' ? '警告' : '信息'}
            </button>
          ))}
        </div>
      </div>

      {/* Log entries */}
      <div className="space-y-1">
        {allEntries.length === 0 && (
          <div className="text-center text-sm text-gray-400 py-12">
            暂无日志记录
          </div>
        )}
        {filtered.map(entry => {
          const config = LEVEL_CONFIG[entry.level];
          const isExpanded = expandedId === entry.id;
          const sourceIcon = entry.source === 'backend'
            ? <Server size={12} className="text-purple-500 flex-shrink-0" />
            : <Cloud size={12} className="text-blue-400 flex-shrink-0" />;
          const sourceBadge = entry.source === 'backend'
            ? <span className="text-[9px] px-1.5 py-0.5 rounded bg-purple-100 dark:bg-purple-500/20 text-purple-600 dark:text-purple-400 font-bold flex-shrink-0">后端</span>
            : <span className="text-[9px] px-1.5 py-0.5 rounded bg-blue-100 dark:bg-blue-500/20 text-blue-600 dark:text-blue-400 font-bold flex-shrink-0">前端</span>;
          return (
            <div
              key={entry.id}
              className={`rounded-lg border ${config.bg} ${
                isExpanded ? 'border-gray-300 dark:border-gray-600' : 'border-transparent'
              } transition-colors`}
            >
              <button
                onClick={() => setExpandedId(isExpanded ? null : entry.id)}
                className="w-full flex items-center gap-2 px-3 py-2 text-left"
              >
                <span className={`flex-shrink-0 ${config.color}`}>{config.icon}</span>
                {sourceIcon}
                {sourceBadge}
                <span className="text-[10px] text-gray-400 font-mono flex-shrink-0">
                  {formatTime(entry.timestamp)}
                </span>
                <span className="text-[10px] text-gray-400 font-mono flex-shrink-0">
                  {entry.method}
                </span>
                <span className="text-xs text-gray-600 dark:text-gray-300 truncate flex-1 font-mono">
                  {formatUrl(entry.url)}
                </span>
                {entry.status !== null && (
                  <span className={`text-[10px] font-mono font-bold flex-shrink-0 ${
                    entry.status >= 500 ? 'text-red-500' :
                    entry.status >= 400 ? 'text-amber-500' :
                    'text-gray-400'
                  }`}>
                    {entry.status}
                  </span>
                )}
                {entry.nonJsonResponse && (
                  <span className="text-[9px] px-1.5 py-0.5 rounded bg-red-100 dark:bg-red-500/20 text-red-600 dark:text-red-400 font-bold flex-shrink-0">
                    非JSON
                  </span>
                )}
                <span className="text-[10px] text-gray-400 font-mono flex-shrink-0">
                  {entry.durationMs}ms
                </span>
                <ChevronDown
                  size={14}
                  className={`text-gray-400 flex-shrink-0 transition-transform ${isExpanded ? 'rotate-180' : ''}`}
                />
              </button>

              {isExpanded && (
                <div className="px-3 pb-3 space-y-2">
                  {entry.provider && (
                    <div className="text-[10px] text-gray-500">
                      Provider: <span className="font-mono">{entry.provider}</span>
                      {entry.model && <> · Model: <span className="font-mono">{entry.model}</span></>}
                      {entry.feature && <> · <span className="font-mono text-[#07c160]">{entry.feature}</span></>}
                    </div>
                  )}
                  {entry.error && (
                    <SnippetBlock label="错误信息" content={entry.error} bg="bg-red-50 dark:bg-red-500/10" />
                  )}
                  {entry.requestSnippet && (
                    <SnippetBlock label="请求体" content={entry.requestSnippet} />
                  )}
                  {entry.responseSnippet && (
                    <SnippetBlock label="响应体" content={entry.responseSnippet} />
                  )}
                  {!entry.error && !entry.requestSnippet && !entry.responseSnippet && (
                    <div className="text-xs text-gray-400">无额外信息</div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
};
