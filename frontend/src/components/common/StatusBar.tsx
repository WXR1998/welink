import React, { useEffect, useState, useRef, useCallback } from 'react';
import { Activity, Zap } from 'lucide-react';
import api from '../../services/api';

interface TokenUsage {
  model: string;
  kind: string;
  prompt_tokens: number;
  output_tokens: number;
  total_tokens: number;
  call_count: number;
  is_embedding: boolean;
}

interface Job {
  key: string;
  step: string;
  current: number;
  total: number;
  done: boolean;
  paused: boolean;
  error: string;
  fact_count: number;
}

interface BatchTask {
  id: number;
  contact_key: string;
  username: string;
  is_group: boolean;
  status: string;
  current_step?: string;
  progress_done?: number;
  progress_total?: number;
  error?: string;
}

function toM(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return String(n);
}

const stepLabel: Record<string, string> = {
  embedding: '向量编码',
  extracting: '记忆提炼',
  done: '完成',
  error: '错误',
  paused: '已暂停',
};

export const StatusBar: React.FC = () => {
  const [totalTokens, setTotalTokens] = useState(0);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [batchTasks, setBatchTasks] = useState<BatchTask[]>([]);
  const [flash, setFlash] = useState(false);
  const prevCallCountRef = useRef(0);
  const flashTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const pollAll = useCallback(async () => {
    const promises: Promise<void>[] = [];

    // Token stats
    promises.push((async () => {
      try {
        const r = await api.get<unknown, {
          usage: TokenUsage[];
          daily: TokenUsage[];
          recent_speeds: { kind: string; speed: number; count: number }[];
        }>('/token-stats');
        const total = (r.usage || []).reduce((s, u) => s + u.total_tokens, 0);
        const totalCalls = (r.usage || []).reduce((s, u) => s + u.call_count, 0);
        setTotalTokens(total);

        // Flash on new LLM calls
        if (totalCalls > prevCallCountRef.current) {
          setFlash(true);
          if (flashTimerRef.current) clearTimeout(flashTimerRef.current);
          flashTimerRef.current = setTimeout(() => setFlash(false), 600);
        }
        prevCallCountRef.current = totalCalls;
      } catch { /* ignore */ }
    })());

    // Vec jobs
    promises.push((async () => {
      try {
        const r = await api.get<unknown, { jobs: Job[] }>('/ai/vec/all-jobs');
        setJobs(r.jobs || []);
      } catch { /* ignore */ }
    })());

    // Batch tasks
    promises.push((async () => {
      try {
        const r = await api.get<unknown, { tasks: BatchTask[] }>('/ai/mem/batch');
        setBatchTasks(r.tasks || []);
      } catch { /* ignore */ }
    })());

    await Promise.all(promises);
  }, []);

  useEffect(() => {
    void pollAll();
    const timer = setInterval(pollAll, 3000);
    return () => {
      clearInterval(timer);
      if (flashTimerRef.current) clearTimeout(flashTimerRef.current);
    };
  }, [pollAll]);

  // Collect running task descriptions
  const runningTasks: string[] = [];

  // Running vec jobs (embedding / extracting)
  for (const job of jobs) {
    if (job.done) continue;
    if (job.paused) continue;
    const label = stepLabel[job.step] || job.step;
    if (job.step === 'embedding' || job.step === 'extracting') {
      const progress = job.total > 0 ? ` ${job.current}/${job.total}` : '';
      runningTasks.push(`${label}${progress}`);
    }
  }

  // Running batch tasks
  for (const t of batchTasks) {
    if (t.status !== 'running') continue;
    const stepLabels: Record<string, string> = {
      fts: '构建索引',
      vec_index: '向量编码',
      mem_extraction: '记忆提炼',
    };
    const label = stepLabels[t.current_step || ''] || t.current_step || '处理中';
    const total = t.progress_total || 0;
    const done = t.progress_done || 0;
    const progress = total > 0 ? ` ${done}/${total}` : '';
    runningTasks.push(`${label}${progress}`);
  }

  return (
    <div className="fixed bottom-0 left-0 right-0 z-[6000] h-9 bg-white/95 dark:bg-[#1d1d1f]/95 backdrop-blur-md border-t border-gray-200 dark:border-white/10 flex items-center px-4 gap-4 text-xs">
      {/* Left: running tasks */}
      <div className="flex items-center gap-2 min-w-0 flex-1">
        <Activity size={12} className={`shrink-0 ${flash ? 'text-[#07c160]' : 'text-gray-400'}`} />
        {runningTasks.length > 0 ? (
          <span className="truncate text-gray-600 dark:text-gray-300">
            {runningTasks.join(' · ')}
          </span>
        ) : (
          <span className="text-gray-400">空闲</span>
        )}
      </div>

      {/* Middle: memory count placeholder (filled by parent via context) */}
      <div className="flex items-center gap-3 text-gray-500 dark:text-gray-400 shrink-0">
        <MemoryCountDisplay />
      </div>

      {/* Right: token count with flash */}
      <div className="flex items-center gap-1.5 shrink-0">
        <Zap
          size={12}
          className={flash ? 'text-[#07c160] transition-colors' : 'text-gray-400 transition-colors'}
          fill={flash ? 'currentColor' : 'none'}
        />
        <span className={`font-semibold tabular-nums ${flash ? 'text-[#07c160]' : 'text-gray-600 dark:text-gray-300'}`}>
          {toM(totalTokens)}
        </span>
      </div>
    </div>
  );
};

// Memory count is provided by a global event bus so StatusBar can display it
// without prop drilling from MemoryLibraryPage.
let _memTotal = 0;
let _memPinned = 0;
const _memListeners = new Set<() => void>();

export function setMemoryCount(total: number, pinned: number) {
  _memTotal = total;
  _memPinned = pinned;
  _memListeners.forEach(fn => fn());
}

const MemoryCountDisplay: React.FC = () => {
  const [, force] = useState(0);
  useEffect(() => {
    const fn = () => force(n => n + 1);
    _memListeners.add(fn);
    return () => { _memListeners.delete(fn); };
  }, []);
  return (
    <>
      <span className="tabular-nums">{_memTotal}</span>
      <span className="text-gray-400">条记忆</span>
      {_memPinned > 0 && (
        <>
          <span className="text-gray-300">·</span>
          <span className="tabular-nums text-amber-500">{_memPinned}</span>
          <span className="text-gray-400">置顶</span>
        </>
      )}
    </>
  );
};
