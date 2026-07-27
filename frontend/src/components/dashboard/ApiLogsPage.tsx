/**
 * API 日志页面 — 展示所有被拦截的 fetch 请求/响应日志
 * 用于调试 "Unexpected token '<'" 等后端返回非 JSON 的问题
 */

import React, { useState, useEffect, useCallback } from 'react';
import { Trash2, ChevronDown, AlertCircle, Info, AlertTriangle, Search } from 'lucide-react';
import {
  type ApiLogEntry,
  type LogLevel,
  getEntries,
  clearEntries,
  subscribe,
} from '../../utils/apiLogger';

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

export const ApiLogsPage: React.FC = () => {
  const [entries, setEntries] = useState<ApiLogEntry[]>([]);
  const [filter, setFilter] = useState<'all' | 'error' | 'warn' | 'info'>('all');
  const [search, setSearch] = useState('');
  const [expandedId, setExpandedId] = useState<number | null>(null);

  useEffect(() => {
    setEntries(getEntries());
    const unsub = subscribe(() => setEntries([...getEntries()]));
    return unsub;
  }, []);

  const handleClear = useCallback(() => {
    clearEntries();
  }, []);

  const filtered = entries.filter(e => {
    if (filter !== 'all' && e.level !== filter) return false;
    if (search) {
      const s = search.toLowerCase();
      return e.url.toLowerCase().includes(s) ||
             e.error.toLowerCase().includes(s) ||
             e.responseSnippet.toLowerCase().includes(s) ||
             e.requestSnippet.toLowerCase().includes(s);
    }
    return true;
  });

  const errorCount = entries.filter(e => e.level === 'error').length;
  const warnCount = entries.filter(e => e.level === 'warn').length;
  const infoCount = entries.filter(e => e.level === 'info').length;

  return (
    <div className="max-w-5xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div>
          <h1 className="text-xl font-bold text-gray-800 dark:text-gray-100">API 日志</h1>
          <p className="text-xs text-gray-400 mt-0.5">
            共 {entries.length} 条 · 错误 {errorCount} · 警告 {warnCount} · 信息 {infoCount}
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
          {(['all', 'error', 'warn', 'info'] as const).map(f => (
            <button
              key={f}
              onClick={() => setFilter(f)}
              className={`px-2.5 py-1.5 rounded-lg text-xs font-medium transition-colors ${
                filter === f
                  ? 'bg-[#07c160] text-white'
                  : 'text-gray-500 hover:bg-gray-100 dark:hover:bg-white/10'
              }`}
            >
              {f === 'all' ? '全部' : f === 'error' ? '错误' : f === 'warn' ? '警告' : '信息'}
            </button>
          ))}
        </div>
      </div>

      {/* Log entries */}
      <div className="space-y-1">
        {filtered.length === 0 && (
          <div className="text-center text-sm text-gray-400 py-12">
            {entries.length === 0 ? '暂无日志记录' : '没有匹配的日志'}
          </div>
        )}
        {filtered.map(entry => {
          const config = LEVEL_CONFIG[entry.level];
          const isExpanded = expandedId === entry.id;
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
                  {entry.error && (
                    <div>
                      <div className="text-[10px] text-gray-400 font-bold mb-1">错误信息</div>
                      <pre className="text-xs text-red-600 dark:text-red-400 bg-red-50 dark:bg-red-500/10 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all">
                        {entry.error}
                      </pre>
                    </div>
                  )}
                  {entry.requestSnippet && (
                    <div>
                      <div className="text-[10px] text-gray-400 font-bold mb-1">请求体</div>
                      <pre className="text-xs text-gray-600 dark:text-gray-300 bg-gray-50 dark:bg-white/5 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all">
                        {entry.requestSnippet}
                      </pre>
                    </div>
                  )}
                  {entry.responseSnippet && (
                    <div>
                      <div className="text-[10px] text-gray-400 font-bold mb-1">
                        响应体{entry.nonJsonResponse ? '（⚠️ 后端返回了非 JSON 内容）' : ''}
                      </div>
                      <pre className="text-xs text-gray-600 dark:text-gray-300 bg-gray-50 dark:bg-white/5 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all">
                        {entry.responseSnippet}
                      </pre>
                    </div>
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
