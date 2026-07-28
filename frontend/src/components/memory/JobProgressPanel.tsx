import React, { useEffect, useState } from 'react';
import { Loader2, Square, ChevronDown, ChevronUp, Pin, PinOff, Trash2, Check, X as XIcon, Pencil } from 'lucide-react';
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
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState(() => window.innerWidth < 640);
  const [factList, setFactList] = useState<MemFact[]>([]);
  const [factLoading, setFactLoading] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [editDraft, setEditDraft] = useState('');

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

  const fetchFacts = async (contactKey: string) => {
    setFactLoading(true);
    try {
      const r = await axios.get<{ facts: MemFact[]; total: number }>(
        `/api/memory/list?contact=${encodeURIComponent(contactKey)}&limit=500`
      );
      setFactList(r.data.facts || []);
    } catch {
      setFactList([]);
    } finally {
      setFactLoading(false);
    }
  };

  const toggleExpand = (key: string) => {
    if (expandedKey === key) {
      setExpandedKey(null);
      return;
    }
    setExpandedKey(key);
    void fetchFacts(key);
  };

  const togglePin = async (f: MemFact) => {
    await axios.put(`/api/memory/${f.id}/pin`, { pinned: !f.pinned });
    setFactList(list => list.map(x => x.id === f.id ? { ...x, pinned: !x.pinned } : x));
  };

  const deleteFact = async (f: MemFact) => {
    if (!confirm(`删除这条记忆？\n\n"${f.fact.slice(0, 60)}${f.fact.length > 60 ? '…' : ''}"`)) return;
    await axios.delete(`/api/memory/${f.id}`);
    setFactList(list => list.filter(x => x.id !== f.id));
  };

  const startEdit = (f: MemFact) => { setEditingId(f.id); setEditDraft(f.fact); };
  const cancelEdit = () => { setEditingId(null); setEditDraft(''); };
  const saveEdit = async (id: number) => {
    const v = editDraft.trim();
    if (!v) return;
    await axios.put(`/api/memory/${id}`, { fact: v });
    setFactList(list => list.map(x => x.id === id ? { ...x, fact: v } : x));
    cancelEdit();
  };

  if (jobs.length === 0) return null;

  return (
    <div className="mb-4 rounded-2xl bg-white dark:bg-[#1d1d1f] border border-gray-100 dark:border-white/10 p-4">
      <button
        onClick={() => setCollapsed(c => !c)}
        className="w-full flex items-center justify-between"
      >
        <div className="flex items-center gap-2">
          <Loader2 size={14} className="animate-spin text-[#07c160]" />
          <span className="text-sm font-bold dk-text">运行中的任务</span>
          <span className="text-xs text-gray-400">{jobs.length} 个</span>
        </div>
        {collapsed ? <ChevronDown size={14} className="text-gray-400" /> : <ChevronUp size={14} className="text-gray-400" />}
      </button>
      {!collapsed && (
        <>
          <div className="flex items-center justify-between mb-3 mt-2">
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
              const isExpanded = expandedKey === job.key;
              return (
                <div key={job.key}>
                  <button
                    onClick={() => toggleExpand(job.key)}
                    className="w-full flex items-center gap-3 px-3 py-2 rounded-xl bg-gray-50 dark:bg-white/5 hover:bg-gray-100 dark:hover:bg-white/10 transition-colors"
                  >
                    {info.avatar
                      ? <img src={info.avatar} alt="" className="w-7 h-7 rounded-lg object-cover shrink-0" />
                      : <div className="w-7 h-7 rounded-lg bg-gray-200 dark:bg-white/10 shrink-0 flex items-center justify-center text-[10px]">{info.isGroup ? '群' : '人'}</div>
                    }
                    <div className="flex-1 min-w-0 text-left">
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
                        {isExpanded ? <ChevronUp size={12} className="text-gray-400 shrink-0" /> : <ChevronDown size={12} className="text-gray-400 shrink-0" />}
                      </div>
                    </div>
                  </button>
                  {isExpanded && (
                    <div className="mt-1 px-2 py-2 space-y-1">
                      {factLoading ? (
                        <div className="flex items-center justify-center py-4">
                          <Loader2 size={14} className="animate-spin text-gray-400" />
                        </div>
                      ) : factList.length === 0 ? (
                        <p className="text-xs text-gray-400 text-center py-3">还没有记忆</p>
                      ) : (
                        factList.map(f => (
                          <div key={f.id} className="flex items-start gap-2 px-2 py-1.5 rounded-lg hover:bg-gray-50 dark:hover:bg-white/5 group">
                            {editingId === f.id ? (
                              <div className="flex-1 flex items-start gap-1">
                                <textarea
                                  value={editDraft}
                                  onChange={e => setEditDraft(e.target.value)}
                                  rows={2}
                                  className="flex-1 px-2 py-1 text-xs rounded-lg bg-white dark:bg-white/5 border border-gray-200 dark:border-white/10 dk-text outline-none focus:border-[#07c160] resize-none"
                                />
                                <button onClick={() => saveEdit(f.id)} className="text-[#07c160] hover:text-[#06ad56]"><Check size={12} /></button>
                                <button onClick={cancelEdit} className="text-gray-400 hover:text-gray-600"><XIcon size={12} /></button>
                              </div>
                            ) : (
                              <>
                                <span className={`text-xs leading-relaxed flex-1 ${f.pinned ? 'font-semibold text-[#07c160]' : 'text-gray-600 dark:text-gray-300'}`}>
                                  {f.fact}
                                </span>
                                <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity">
                                  <button onClick={() => togglePin(f)} className={`p-1 rounded hover:bg-gray-100 dark:hover:bg-white/10 ${f.pinned ? 'text-[#07c160]' : 'text-gray-300'}`}>
                                    {f.pinned ? <PinOff size={11} /> : <Pin size={11} />}
                                  </button>
                                  <button onClick={() => startEdit(f)} className="p-1 rounded hover:bg-gray-100 dark:hover:bg-white/10 text-gray-300 hover:text-gray-500">
                                    <Pencil size={11} />
                                  </button>
                                  <button onClick={() => deleteFact(f)} className="p-1 rounded hover:bg-gray-100 dark:hover:bg-white/10 text-gray-300 hover:text-red-500">
                                    <Trash2 size={11} />
                                  </button>
                                </div>
                              </>
                            )}
                          </div>
                        ))
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </>
      )}
    </div>
  );
};
