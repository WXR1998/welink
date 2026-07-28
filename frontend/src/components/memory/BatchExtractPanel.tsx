import React, { useState, useEffect, useCallback } from 'react';
import { Loader2, Plus, X, Check, Play, Trash2, Layers } from 'lucide-react';
import axios from 'axios';
import type { ContactStats, GroupInfo } from '../../types';
import { avatarSrc } from '../../utils/avatar';

interface BatchTask {
  id: number;
  contact_key: string;
  username: string;
  is_group: boolean;
  status: string;
  error?: string;
  created_at: number;
  updated_at: number;
}

interface Props {
  contacts: ContactStats[];
  groups: GroupInfo[];
}

export const BatchExtractPanel: React.FC<Props> = ({ contacts, groups }) => {
  const [mode, setMode] = useState<'idle' | 'select-contacts'>('idle');
  const [selectedContacts, setSelectedContacts] = useState<Set<string>>(new Set());
  const [foundGroups, setFoundGroups] = useState<GroupInfo[]>([]);
  const [selectedGroups, setSelectedGroups] = useState<Set<string>>(new Set());
  const [searching, setSearching] = useState(false);
  const [enqueuing, setEnqueuing] = useState(false);
  const [tasks, setTasks] = useState<BatchTask[]>([]);
  const [contactQuery, setContactQuery] = useState('');

  const nameMap = React.useMemo(() => {
    const m = new Map<string, { name: string; avatar?: string }>();
    for (const c of contacts) {
      m.set(c.username, {
        name: c.remark || c.nickname || c.username,
        avatar: avatarSrc(c.small_head_url),
      });
    }
    return m;
  }, [contacts]);

  const filteredContacts = React.useMemo(() => {
    return contacts.filter(c => {
      const name = c.remark || c.nickname || c.username;
      return name.toLowerCase().includes(contactQuery.toLowerCase());
    });
  }, [contacts, contactQuery]);

  const fetchTasks = useCallback(async () => {
    try {
      const r = await axios.get<{ tasks: BatchTask[] }>('/api/ai/mem/batch');
      setTasks(r.data.tasks || []);
    } catch { /* ignore */ }
  }, []);

  useEffect(() => {
    fetchTasks();
    const timer = setInterval(fetchTasks, 2000);
    return () => clearInterval(timer);
  }, [fetchTasks]);

  const toggleContact = (username: string) => {
    setSelectedContacts(prev => {
      const next = new Set(prev);
      if (next.has(username)) next.delete(username);
      else next.add(username);
      return next;
    });
  };

  const toggleGroup = (username: string) => {
    setSelectedGroups(prev => {
      const next = new Set(prev);
      if (next.has(username)) next.delete(username);
      else next.add(username);
      return next;
    });
  };

  const handleFindGroups = async () => {
    if (selectedContacts.size === 0) return;
    setSearching(true);
    setFoundGroups([]);
    setSelectedGroups(new Set());
    try {
      const r = await axios.post<{ groups: GroupInfo[] }>(
        '/api/contacts/common-groups-for-many',
        { usernames: Array.from(selectedContacts) },
      );
      setFoundGroups(r.data.groups || []);
    } catch { /* ignore */ }
    finally { setSearching(false); }
  };

  const handleEnqueue = async () => {
    if (selectedGroups.size === 0) return;
    setEnqueuing(true);
    try {
      const tasks = Array.from(selectedGroups).map(g => ({
        contact_key: `group:${g}`,
        username: g,
        is_group: true,
      }));
      await axios.post('/api/ai/mem/batch', { tasks });
      // Reset
      setMode('idle');
      setSelectedContacts(new Set());
      setFoundGroups([]);
      setSelectedGroups(new Set());
      await fetchTasks();
    } catch { /* ignore */ }
    finally { setEnqueuing(false); }
  };

  const handleDeleteTask = async (id: number) => {
    await axios.delete(`/api/ai/mem/batch/${id}`);
    await fetchTasks();
  };

  const handleClearFinished = async () => {
    await axios.delete('/api/ai/mem/batch');
    await fetchTasks();
  };

  const pendingCount = tasks.filter(t => t.status === 'pending' || t.status === 'running').length;
  const doneCount = tasks.filter(t => t.status === 'done').length;
  const errorCount = tasks.filter(t => t.status === 'error').length;

  return (
    <div className="mb-4 rounded-2xl bg-white dark:bg-[#1d1d1f] border border-gray-100 dark:border-white/10 p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Layers size={14} className="text-[#07c160]" />
          <span className="text-sm font-bold dk-text">批量提炼</span>
          {tasks.length > 0 && (
            <span className="text-xs text-gray-400">
              {pendingCount} 待处理 · {doneCount} 完成 · {errorCount} 失败
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {mode === 'idle' && (
            <button
              onClick={() => setMode('select-contacts')}
              className="flex items-center gap-1 px-3 py-1.5 text-xs font-bold border border-gray-200 dark:border-white/10 rounded-xl hover:border-[#07c160] hover:text-[#07c160] transition-colors dk-text"
            >
              <Plus size={12} />
              添加批量任务
            </button>
          )}
          {tasks.some(t => t.status === 'done' || t.status === 'error') && (
            <button
              onClick={handleClearFinished}
              className="flex items-center gap-1 px-2 py-1.5 text-xs text-gray-400 hover:text-red-500 transition-colors"
            >
              <Trash2 size={11} />
              清除已完成
            </button>
          )}
        </div>
      </div>

      {/* Task list */}
      {tasks.length > 0 && (
        <div className="space-y-1 mb-3">
          {tasks.map(t => {
            const name = nameMap.get(t.username)?.name || t.username;
            const statusConfig: Record<string, { label: string; color: string }> = {
              pending: { label: '排队中', color: 'text-gray-400' },
              running: { label: '处理中', color: 'text-[#07c160]' },
              done: { label: '完成', color: 'text-blue-500' },
              error: { label: '失败', color: 'text-red-500' },
            };
            const sc = statusConfig[t.status] || statusConfig.pending;
            return (
              <div key={t.id} className="flex items-center gap-2 px-2 py-1.5 rounded-lg hover:bg-gray-50 dark:hover:bg-white/5 group">
                <span className="text-xs text-gray-600 dark:text-gray-300 truncate flex-1">{name}</span>
                {t.status === 'running' && <Loader2 size={10} className="animate-spin text-[#07c160]" />}
                <span className={`text-[10px] font-bold ${sc.color}`}>{sc.label}</span>
                {t.error && <span className="text-[10px] text-red-400 truncate max-w-32" title={t.error}>{t.error}</span>}
                <button onClick={() => handleDeleteTask(t.id)} className="opacity-0 group-hover:opacity-100 text-gray-300 hover:text-red-500 transition-opacity">
                  <X size={11} />
                </button>
              </div>
            );
          })}
        </div>
      )}

      {/* Contact selector mode */}
      {mode === 'select-contacts' && (
        <div className="border-t border-gray-100 dark:border-white/10 pt-3">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-bold dk-text">选择联系人（找出共同群聊）</span>
            <button onClick={() => setMode('idle')} className="text-xs text-gray-400 hover:text-gray-600"><X size={14} /></button>
          </div>

          <input
            type="text"
            value={contactQuery}
            onChange={e => setContactQuery(e.target.value)}
            placeholder="搜索联系人..."
            className="w-full mb-2 px-3 py-1.5 text-sm border border-gray-200 dark:border-white/10 rounded-lg focus:outline-none focus:border-[#07c160] dk-input"
          />

          <div className="max-h-48 overflow-y-auto space-y-0.5 mb-3">
            {filteredContacts.slice(0, 100).map(c => {
              const name = c.remark || c.nickname || c.username;
              const selected = selectedContacts.has(c.username);
              return (
                <button
                  key={c.username}
                  onClick={() => toggleContact(c.username)}
                  className={`w-full text-left px-2 py-1.5 rounded-lg text-sm flex items-center gap-2 transition-colors ${
                    selected ? 'bg-[#07c160]/10 text-[#07c160]' : 'hover:bg-gray-50 dark:hover:bg-white/5'
                  }`}
                >
                  {selected ? <Check size={14} /> : <div className="w-3.5" />}
                  {c.small_head_url && <img src={avatarSrc(c.small_head_url)} alt="" className="w-5 h-5 rounded" />}
                  <span className="truncate">{name}</span>
                </button>
              );
            })}
          </div>

          {selectedContacts.size > 0 && (
            <div className="flex items-center gap-2 mb-3">
              <span className="text-xs text-gray-400">已选 {selectedContacts.size} 人</span>
              <button
                onClick={handleFindGroups}
                disabled={searching}
                className="ml-auto flex items-center gap-1 px-3 py-1.5 text-xs font-bold bg-[#07c160] text-white rounded-lg hover:bg-[#06ad56] disabled:opacity-50"
              >
                {searching ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />}
                查找共同群聊
              </button>
            </div>
          )}

          {/* Found groups */}
          {foundGroups.length > 0 && (
            <div className="border-t border-gray-100 dark:border-white/10 pt-3">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-bold dk-text">选择群聊（{foundGroups.length} 个共同群）</span>
              </div>
              <div className="max-h-48 overflow-y-auto space-y-0.5 mb-3">
                {foundGroups.map(g => {
                  const selected = selectedGroups.has(g.username);
                  return (
                    <button
                      key={g.username}
                      onClick={() => toggleGroup(g.username)}
                      className={`w-full text-left px-2 py-1.5 rounded-lg text-sm flex items-center gap-2 transition-colors ${
                        selected ? 'bg-[#07c160]/10 text-[#07c160]' : 'hover:bg-gray-50 dark:hover:bg-white/5'
                      }`}
                    >
                      {selected ? <Check size={14} /> : <div className="w-3.5" />}
                      <span className="truncate">{g.name || g.username}</span>
                      <span className="text-xs text-gray-400 ml-auto">{g.total_messages} 条</span>
                    </button>
                  );
                })}
              </div>
              {selectedGroups.size > 0 && (
                <button
                  onClick={handleEnqueue}
                  disabled={enqueuing}
                  className="w-full flex items-center justify-center gap-1.5 py-2 rounded-xl bg-[#07c160] text-white text-sm font-bold hover:bg-[#06ad56] disabled:opacity-50"
                >
                  {enqueuing ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />}
                  提交 {selectedGroups.size} 个提炼任务
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
};
