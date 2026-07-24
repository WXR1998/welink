import React, { useEffect, useState, useRef } from 'react';
import { Activity } from 'lucide-react';
import api from '../../services/api';

interface TokenUsage {
  model: string;
  kind: string; // "chat" / "summary" / "embedding"
  prompt_tokens: number;
  output_tokens: number;
  total_tokens: number;
  call_count: number;
  is_embedding: boolean;
}

function toM(n: number): string {
  return (n / 1_000_000).toFixed(2) + 'M';
}

const CATEGORY_LABELS: Record<string, string> = {
  chat: 'AI 对话',
  summary: '记忆总结',
  embedding: 'Embedding',
};

const CATEGORY_ORDER = ['chat', 'summary', 'embedding'];

export const TokenStatsWidget: React.FC = () => {
  const [usage, setUsage] = useState<TokenUsage[]>([]);
  const [expanded, setExpanded] = useState(false);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchStats = async () => {
    try {
      const r = await api.get<{ usage: TokenUsage[] }>('/token-stats');
      setUsage(r.data.usage || []);
    } catch {
      /* ignore */
    }
  };

  useEffect(() => {
    void fetchStats();
    timerRef.current = setInterval(() => { void fetchStats(); }, 1000);
    return () => { if (timerRef.current) clearInterval(timerRef.current); };
  }, []);

  // Group by kind
  const byKind = new Map<string, TokenUsage[]>();
  for (const u of usage) {
    const arr = byKind.get(u.kind) || [];
    arr.push(u);
    byKind.set(u.kind, arr);
  }

  const totalAll = usage.reduce((s, u) => s + u.total_tokens, 0);

  if (totalAll === 0 && !expanded) return null;

  return (
    <div
      className="fixed bottom-4 right-4 z-[7000]"
      onMouseEnter={() => setExpanded(true)}
      onMouseLeave={() => setExpanded(false)}
    >
      <div className={`bg-white/95 dark:bg-[#1d1d1f]/95 backdrop-blur-md rounded-2xl shadow-2xl border border-gray-200 dark:border-white/10 transition-all duration-200 overflow-hidden ${
        expanded ? 'w-[360px]' : 'w-auto'
      }`}>
        {/* Compact bar */}
        <div className="flex items-center gap-2 px-3 py-2">
          <Activity size={14} className="text-[#07c160]" />
          <span className="text-xs font-semibold text-gray-700 dark:text-gray-200">
            {toM(totalAll)}
          </span>
        </div>

        {/* Expanded details */}
        {expanded && (
          <div className="border-t border-gray-100 dark:border-white/10 p-3 space-y-3 max-h-[450px] overflow-y-auto">
            {CATEGORY_ORDER.map(kind => {
              const items = byKind.get(kind);
              if (!items || items.length === 0) return null;
              const subtotal = items.reduce((s, u) => s + u.total_tokens, 0);
              return (
                <div key={kind}>
                  <div className="flex items-center justify-between mb-1.5">
                    <span className="text-[10px] font-bold text-gray-400 uppercase tracking-wide">{CATEGORY_LABELS[kind]}</span>
                    <span className="text-[10px] font-semibold text-gray-500 dark:text-gray-400">{toM(subtotal)}</span>
                  </div>
                  <div className="space-y-1">
                    {items.map(u => (
                      <div key={u.model} className="flex items-center justify-between text-[11px]">
                        <span className="text-gray-600 dark:text-gray-300 truncate max-w-[160px]" title={u.model}>{u.model}</span>
                        <span className="text-gray-400 shrink-0">
                          <span className="text-gray-500 dark:text-gray-400">{toM(u.prompt_tokens)}→{toM(u.output_tokens)}</span>
                          {' '}
                          <span className="font-semibold text-gray-700 dark:text-gray-200">{toM(u.total_tokens)}</span>
                          {' '}
                          <span className="text-gray-400">({u.call_count}次)</span>
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              );
            })}

            {usage.length === 0 && (
              <div className="text-xs text-gray-400 text-center py-2">暂无 token 使用记录</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
};
