import React, { useEffect, useState, useRef } from 'react';
import { Activity } from 'lucide-react';
import axios from 'axios';

interface TokenUsage {
  model: string;
  prompt_tokens: number;
  output_tokens: number;
  total_tokens: number;
  call_count: number;
  is_embedding: boolean;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return String(n);
}

export const TokenStatsWidget: React.FC = () => {
  const [usage, setUsage] = useState<TokenUsage[]>([]);
  const [expanded, setExpanded] = useState(false);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchStats = async () => {
    try {
      const r = await axios.get<{ usage: TokenUsage[] }>('/api/token-stats');
      setUsage(r.data.usage || []);
    } catch {
      /* ignore */
    }
  };

  useEffect(() => {
    void fetchStats();
    timerRef = setInterval(() => { void fetchStats(); }, 1000);
    return () => { if (timerRef) clearInterval(timerRef); };
  }, []);

  const llmUsage = usage.filter(u => !u.is_embedding);
  const embUsage = usage.filter(u => u.is_embedding);
  const totalLLM = llmUsage.reduce((s, u) => s + u.total_tokens, 0);
  const totalEmb = embUsage.reduce((s, u) => s + u.total_tokens, 0);
  const totalAll = totalLLM + totalEmb;

  if (totalAll === 0 && !expanded) return null;

  return (
    <div
      className="fixed bottom-4 right-4 z-[7000]"
      onMouseEnter={() => setExpanded(true)}
      onMouseLeave={() => setExpanded(false)}
    >
      <div className={`bg-white/95 dark:bg-[#1d1d1f]/95 backdrop-blur-md rounded-2xl shadow-2xl border border-gray-200 dark:border-white/10 transition-all duration-200 overflow-hidden ${
        expanded ? 'w-[340px]' : 'w-auto'
      }`}>
        {/* Compact bar (always visible) */}
        <div className="flex items-center gap-2 px-3 py-2">
          <Activity size={14} className="text-[#07c160]" />
          <span className="text-xs font-semibold text-gray-700 dark:text-gray-200">
            {formatTokens(totalAll)} tokens
          </span>
        </div>

        {/* Expanded details */}
        {expanded && (
          <div className="border-t border-gray-100 dark:border-white/10 p-3 space-y-3 max-h-[400px] overflow-y-auto">
            {llmUsage.length > 0 && (
              <div>
                <div className="flex items-center justify-between mb-1.5">
                  <span className="text-[10px] font-bold text-gray-400 uppercase tracking-wide">AI 对话 / 记忆总结</span>
                  <span className="text-[10px] text-gray-400">{formatTokens(totalLLM)}</span>
                </div>
                <div className="space-y-1">
                  {llmUsage.map(u => (
                    <div key={u.model} className="flex items-center justify-between text-[11px]">
                      <span className="text-gray-600 dark:text-gray-300 truncate max-w-[180px]" title={u.model}>{u.model}</span>
                      <span className="text-gray-400 shrink-0">
                        <span className="text-gray-500 dark:text-gray-400">{formatTokens(u.prompt_tokens)}→{formatTokens(u.output_tokens)}</span>
                        {' '}
                        <span className="font-semibold text-gray-700 dark:text-gray-200">{formatTokens(u.total_tokens)}</span>
                        {' '}
                        <span className="text-gray-400">({u.call_count}次)</span>
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {embUsage.length > 0 && (
              <div>
                <div className="flex items-center justify-between mb-1.5">
                  <span className="text-[10px] font-bold text-gray-400 uppercase tracking-wide">Embedding</span>
                  <span className="text-[10px] text-gray-400">{formatTokens(totalEmb)}</span>
                </div>
                <div className="space-y-1">
                  {embUsage.map(u => (
                    <div key={u.model} className="flex items-center justify-between text-[11px]">
                      <span className="text-gray-600 dark:text-gray-300 truncate max-w-[180px]" title={u.model}>{u.model}</span>
                      <span className="text-gray-400 shrink-0">
                        <span className="font-semibold text-gray-700 dark:text-gray-200">{formatTokens(u.total_tokens)}</span>
                        {' '}
                        <span className="text-gray-400">({u.call_count}次)</span>
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {usage.length === 0 && (
              <div className="text-xs text-gray-400 text-center py-2">暂无 token 使用记录</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
};
