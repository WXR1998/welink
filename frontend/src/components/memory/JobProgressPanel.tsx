import React, { useEffect, useState } from 'react';
import { Loader2, AlertCircle, Square } from 'lucide-react';
import axios from 'axios';
import type { ContactStats, GroupInfo } from '../../types';
import { avatarSrc } from '../../utils/avatar';

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

interface Props {
  contacts: ContactStats[];
  groups: GroupInfo[];
}

const stepLabel: Record<string, string> = {
  'embedding': '向量编码',
  'extracting': '记忆提炼',
  'done': '完成',
  'error': '错误',
  'paused': '已暂停',
};

export const JobProgressPanel: React.FC<Props> = ({ contacts, groups }) => {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [stopping, setStopping] = useState(false);

  // Build a lookup map: raw username → display name + avatar
  const nameMap = React.useMemo(() => {
    const m = new Map<string, { name: string; avatar?: string; isGroup: boolean }>();
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

  const resolveName = (rawKey: string): { name: string; avatar?: string; isGroup: boolean } => {
    // Strip contact:/group: prefix
    const username = rawKey.replace(/^(contact|group):/, '');
    return nameMap.get(username) || { name: username, isGroup: false };
  };

  useEffect(() => {
    let active = true;
    const poll = async () => {
      try {
        const r = await axios.get<{ jobs: Job[] }>('/api/ai/vec/all-jobs');
        if (active) setJobs(r.data.jobs || []);
      } catch { /* ignore */ }
    };
    poll();
    const timer = setInterval(poll, 3000);
    return () => { active = false; clearInterval(timer); };
  }, []);

  const handleStopAll = async () => {
    if (!confirm('确定停止所有正在运行的 embed/记忆提炼任务？')) return;
    setStopping(true);
    try {
      await axios.post('/api/ai/abort-all');
    } catch { /* ignore */ }
    finally { setStopping(false); }
  };

  if (jobs.length === 0) return null;

  return (
    <div className="mb-4 rounded-2xl bg-white dark:bg-[#1d1d1f] border border-gray-100 dark:border-white/10 p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Loader2 size={14} className="animate-spin text-[#07c160]" />
          <span className="text-sm font-bold dk-text">运行中的任务</span>
          <span className="text-xs text-gray-400">{jobs.length} 个</span>
        </div>
        <button
          onClick={handleStopAll}
          disabled={stopping}
          className="flex items-center gap-1 px-3 py-1.5 text-xs font-bold border border-red-200 dark:border-red-500/30 text-red-600 dark:text-red-400 rounded-xl hover:bg-red-50 dark:hover:bg-red-500/10 disabled:opacity-50 transition-colors"
        >
          {stopping ? <Loader2 size={12} className="animate-spin" /> : <Square size={12} />}
          停止全部
        </button>
      </div>
      <div className="space-y-2">
        {jobs.map(job => {
          const info = resolveName(job.key);
          const pct = job.total > 0 ? Math.round((job.current / job.total) * 100) : 0;
          const label = stepLabel[job.step] || job.step || '未知';
          const isActive = !job.done && !job.paused && !job.error;
          return (
            <div key={job.key} className="flex items-center gap-3 px-3 py-2 rounded-xl bg-gray-50 dark:bg-white/5">
              {info.avatar
                ? <img src={info.avatar} alt="" className="w-7 h-7 rounded-lg object-cover shrink-0" />
                : <div className="w-7 h-7 rounded-lg bg-gray-200 dark:bg-white/10 shrink-0 flex items-center justify-center text-[10px]">{info.isGroup ? '群' : '人'}</div>
              }
              <div className="flex-1 min-w-0">
                <div className="flex items-center justify-between gap-2 mb-1">
                  <span className="text-xs font-semibold text-gray-700 dark:text-gray-300 truncate">{info.name}</span>
                  <span className={`text-[10px] font-bold px-1.5 py-0.5 rounded shrink-0 ${isActive ? 'bg-[#07c160]/10 text-[#07c160]' : job.done ? 'bg-blue-100 text-blue-600' : job.paused ? 'bg-yellow-100 text-yellow-700' : 'bg-red-100 text-red-600'}`}>
                    {label}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <div className="flex-1 h-1.5 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
                    <div
                      className={`h-full rounded-full transition-all ${isActive ? 'bg-[#07c160]' : job.done ? 'bg-blue-500' : job.paused ? 'bg-yellow-400' : 'bg-red-400'}`}
                      style={{ width: `${pct}%` }}
                    />
                  </div>
                  <span className="text-[10px] text-gray-400 whitespace-nowrap">
                    {job.current}/{job.total}{job.fact_count > 0 ? ` · ${job.fact_count}条` : ''}
                  </span>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};
