import React, { useEffect, useState, useRef, useCallback } from 'react';
import { Activity, Zap } from 'lucide-react';
import api from '../../services/api';
import type { ContactStats, GroupInfo } from '../../types';
import { avatarSrc } from '../../utils/avatar';

interface TokenUsage {
  model: string;
  kind: string;
  prompt_tokens: number;
  output_tokens: number;
  total_tokens: number;
  call_count: number;
  is_embedding: boolean;
}

interface RecentSpeed {
  kind: string;
  speed: number;
  count: number;
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

const CATEGORY_LABELS: Record<string, string> = {
  chat: 'AI 对话',
  summary: '记忆总结',
  embedding: 'Embedding',
};
const CATEGORY_ORDER = ['chat', 'summary', 'embedding'];

const stepLabel: Record<string, string> = {
  embedding: '向量编码',
  extracting: '记忆提炼',
  done: '完成',
  error: '错误',
  paused: '已暂停',
};

const batchStepLabel: Record<string, string> = {
  fts: '构建索引',
  vec_index: '向量编码',
  mem_extraction: '记忆提炼',
};

interface Props {
  contacts: ContactStats[];
  groups: GroupInfo[];
}

export const StatusBar: React.FC<Props> = ({ contacts, groups }) => {
  const [allTime, setAllTime] = useState<TokenUsage[]>([]);
  const [daily, setDaily] = useState<TokenUsage[]>([]);
  const [speeds, setSpeeds] = useState<RecentSpeed[]>([]);

  const [flash, setFlash] = useState(false);

  const prevCallCountRef = useRef(0);
  const flashTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [batchTasks, setBatchTasks] = useState<BatchTask[]>([]);

  const nameMap = React.useMemo(() => {
    const m = new Map<string, { name: string; avatar?: string }>();
    for (const c of contacts) {
      m.set(c.username, {
        name: c.remark || c.nickname || c.username,
        avatar: avatarSrc(c.small_head_url),
      });
    }
    for (const g of groups) {
      m.set(g.username, {
        name: g.name || g.username,
        avatar: avatarSrc(g.small_head_url),
      });
    }
    return m;
  }, [contacts, groups]);

  const pollAll = useCallback(async () => {
    const promises: Promise<void>[] = [];

    promises.push((async () => {
      try {
        const r = await api.get<unknown, {
          usage: TokenUsage[];
          daily: TokenUsage[];
          recent_speeds: RecentSpeed[];
        }>('/token-stats');
        setAllTime(r.usage || []);
        setDaily(r.daily || []);
        setSpeeds(r.recent_speeds || []);
        const totalCalls = (r.usage || []).reduce((s, u) => s + u.call_count, 0);
        if (totalCalls > prevCallCountRef.current) {
          setFlash(true);
          if (flashTimerRef.current) clearTimeout(flashTimerRef.current);
          flashTimerRef.current = setTimeout(() => setFlash(false), 600);
        }
        prevCallCountRef.current = totalCalls;
      } catch { /* ignore */ }
    })());

    promises.push((async () => {
      try {
        const r = await api.get<unknown, { jobs: Job[] }>('/ai/vec/all-jobs');
        setJobs(r.jobs || []);
      } catch { /* ignore */ }
    })());

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

  // Token stats derived data
  const allTimeTotal = allTime.reduce((s, u) => s + u.total_tokens, 0);
  const dailyTotal = daily.reduce((s, u) => s + u.total_tokens, 0);
  const speedMap = new Map(speeds.map(s => [s.kind, s]));

  const buildByKind = (usage: TokenUsage[]) => {
    const m = new Map<string, TokenUsage[]>();
    for (const u of usage) {
      const arr = m.get(u.kind) || [];
      arr.push(u);
      m.set(u.kind, arr);
    }
    return m;
  };

  // Running task info
  const runningBatchTask = batchTasks.find(t => t.status === 'running');
  const pendingCount = batchTasks.filter(t => t.status === 'pending').length;
  const runningCount = batchTasks.filter(t => t.status === 'running').length;
  const queueLen = pendingCount + runningCount;

  const runningVecJobs = jobs.filter(j => !j.done && !j.paused && (j.step === 'embedding' || j.step === 'extracting'));

  // Resolve running batch task name + avatar
  let runningName = '';
  let runningAvatar: string | undefined;
  let runningStepLabel = '';
  let runningProgress = '';
  if (runningBatchTask) {
    const info = nameMap.get(runningBatchTask.username);
    runningName = info?.name || runningBatchTask.username;
    runningAvatar = info?.avatar;
    runningStepLabel = batchStepLabel[runningBatchTask.current_step || ''] || runningBatchTask.current_step || '处理中';
    const total = runningBatchTask.progress_total || 0;
    const done = runningBatchTask.progress_done || 0;
    if (total > 0) runningProgress = `${done}/${total}`;
  }

  return (
    <div
      className="fixed bottom-0 left-0 right-0 z-[6000] h-9 bg-white/95 dark:bg-[#1d1d1f]/95 backdrop-blur-md border-t border-gray-200 dark:border-white/10 flex items-center px-4 gap-4 text-xs"
    >
      {/* Left: running tasks */}
      <div className="flex items-center gap-2 min-w-0 flex-1">
        <Activity size={12} className={`shrink-0 ${flash ? 'text-[#07c160]' : 'text-gray-400'}`} />
        {runningBatchTask ? (
          <>
            {runningAvatar
              ? <img src={runningAvatar} alt="" className="w-5 h-5 rounded object-cover shrink-0" />
              : <div className="w-5 h-5 rounded bg-gray-200 dark:bg-white/10 shrink-0" />}
            <span className="truncate text-gray-600 dark:text-gray-300 max-w-32">{runningName}</span>
            <span className="text-gray-400">·</span>
            <span className="text-gray-500">{runningStepLabel}</span>
            {runningProgress && <span className="text-gray-400 tabular-nums">{runningProgress}</span>}
          </>
        ) : runningVecJobs.length > 0 ? (
          <span className="truncate text-gray-600 dark:text-gray-300">
            {runningVecJobs.map(j => {
              const label = stepLabel[j.step] || j.step;
              const progress = j.total > 0 ? ` ${j.current}/${j.total}` : '';
              return `${label}${progress}`;
            }).join(' · ')}
          </span>
        ) : (
          <span className="text-gray-400">空闲</span>
        )}
        {queueLen > 1 && (
          <span className="text-gray-400 shrink-0 ml-2">队列 {queueLen}</span>
        )}
      </div>

      {/* Middle: memory count */}
      <div className="flex items-center gap-3 text-gray-500 dark:text-gray-400 shrink-0">
        <MemoryCountDisplay />
      </div>

      {/* Right: token stats (daily + all-time, each with hover detail) */}
      <div className="flex items-center gap-3 shrink-0">
        <TokenHoverSection
          label="今日"
          total={dailyTotal}
          flash={flash}
          usage={daily}
          speeds={speeds}
        />
        <span className="text-gray-300">|</span>
        <TokenHoverSection
          label="全量"
          total={allTimeTotal}
          flash={flash}
          usage={allTime}
          speeds={speeds}
        />
      </div>
    </div>
  );
};

const TokenHoverSection: React.FC<{
  label: string;
  total: number;
  flash: boolean;
  usage: TokenUsage[];
  speeds: RecentSpeed[];
}> = ({ label, total, flash, usage, speeds }) => {
  const [hovered, setHovered] = useState(false);
  const byKind = new Map<string, TokenUsage[]>();
  for (const u of usage) {
    const arr = byKind.get(u.kind) || [];
    arr.push(u);
    byKind.set(u.kind, arr);
  }
  const speedMap = new Map(speeds.map(s => [s.kind, s]));

  return (
    <div
      className="flex items-center gap-1 relative cursor-default"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      <Zap
        size={12}
        className={flash ? 'text-[#07c160] transition-colors' : 'text-gray-400 transition-colors'}
        fill={flash ? 'currentColor' : 'none'}
      />
      <span className="text-[10px] text-gray-400">{label}</span>
      <span className={`font-semibold tabular-nums text-[11px] ${flash ? 'text-[#07c160]' : 'text-gray-600 dark:text-gray-300'}`}>
        {toM(total)}
      </span>
      {hovered && (
        <div className="absolute bottom-full right-0 mb-1 w-[340px] bg-white dark:bg-[#1d1d1f] rounded-2xl shadow-2xl border border-gray-200 dark:border-white/10 p-3 space-y-3 max-h-[450px] overflow-y-auto">
          <div className="flex items-center justify-between">
            <span className="text-[10px] font-bold text-gray-400 uppercase tracking-wide">{label}用量</span>
            <span className="text-[10px] font-semibold text-gray-500 dark:text-gray-400">{toM(total)}</span>
          </div>
          {CATEGORY_ORDER.map(kind => {
            const items = (byKind.get(kind) || []).slice().sort((a, b) => b.total_tokens - a.total_tokens).slice(0, 3);
            const sp = speedMap.get(kind);
            const subtotal = items.reduce((s, u) => s + u.total_tokens, 0);
            const hasData = items.length > 0;
            if (!hasData && !sp) return null;
            return (
              <div key={kind}>
                <div className="flex items-center justify-between mb-1.5">
                  <span className="text-[10px] font-bold text-gray-400 uppercase tracking-wide">{CATEGORY_LABELS[kind]}</span>
                  <div className="flex items-center gap-2">
                    {sp && sp.count > 0 && <span className="text-[10px] text-[#07c160] font-semibold">{sp.speed.toFixed(1)} t/s</span>}
                    <span className="text-[10px] font-semibold text-gray-500 dark:text-gray-400">{toM(subtotal)}</span>
                  </div>
                </div>
                {hasData && (
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
                )}
              </div>
            );
          })}
          {usage.length === 0 && (
            <div className="text-xs text-gray-400 text-center py-2">暂无 token 使用记录</div>
          )}
        </div>
      )}
    </div>
  );
};

// Memory count is provided by a global event bus so StatusBar can display it
// without prop drilling from MemoryLibraryPage.
let _memTotal = 0;
let _memPinned = 0;
let _memFlash = false;
let _memFlashTimer: ReturnType<typeof setTimeout> | null = null;
const _memListeners = new Set<() => void>();

export function setMemoryCount(total: number, pinned: number) {
  const grew = total > _memTotal;
  _memTotal = total;
  _memPinned = pinned;
  if (grew) {
    _memFlash = true;
    if (_memFlashTimer) clearTimeout(_memFlashTimer);
    _memFlashTimer = setTimeout(() => { _memFlash = false; _memListeners.forEach(fn => fn()); }, 600);
  }
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
      <span className={`tabular-nums transition-colors ${_memFlash ? 'text-[#07c160]' : ''}`}>{_memTotal}</span>
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
