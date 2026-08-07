import React, { useState, useEffect } from 'react';
import { Loader2, AlertCircle, Check, Trash2, Plus, ChevronUp, ChevronDown, X } from 'lucide-react';
import axios from 'axios';
import { genId, newMemLLMProfile, PROVIDERS, type MemLLMProfile } from './types';

export const MemorySection: React.FC = () => {
  const [profiles, setProfiles] = useState<MemLLMProfile[]>([]);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [clearing, setClearing] = useState<'facts' | 'embeddings' | null>(null);

  useEffect(() => {
    axios.get<Record<string, unknown>>('/api/preferences').then(r => {
      const mps = (r.data.mem_llm_profiles as MemLLMProfile[] | undefined);
      if (mps && mps.length > 0) {
        setProfiles(mps);
      } else {
        setProfiles([]); // 留空时后端复用默认 LLM profile
      }
    }).catch(() => {}).finally(() => setLoaded(true));
  }, []);

  const buildPayload = async () => {
    let fresh: Record<string, unknown> = {};
    try {
      const r = await axios.get<Record<string, unknown>>('/api/preferences');
      fresh = r.data;
    } catch { /* ignore */ }
    return {
      ...fresh,
      mem_llm_profiles: profiles,
    };
  };

  const handleSave = async () => {
    setSaving(true);
    setSaveMsg(null);
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      setSaveMsg({ ok: true, text: '已保存' });
    } catch {
      setSaveMsg({ ok: false, text: '保存失败' });
    } finally {
      setSaving(false);
      setTimeout(() => setSaveMsg(null), 3000);
    }
  };

  const handleMemTest = async () => {
    setTesting(true);
    setSaveMsg(null);
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      const r = await axios.post<{ results: { provider: string; model: string; ok: boolean; latency_ms: number; tokens_per_second: number; error?: string }[] }>('/api/ai/mem/test');
      const results = r.data.results ?? [];
      const okCount = results.filter(r => r.ok).length;
      const failCount = results.length - okCount;
      const detail = results.map(r => {
        if (!r.ok) return `${r.provider}: ${r.error ?? '失败'}`;
        const parts = [`${r.provider}: ${r.latency_ms}ms`];
        if (r.tokens_per_second > 0) parts.push(`${r.tokens_per_second.toFixed(1)} tok/s`);
        return parts.join(' · ');
      }).join('；');
      if (failCount === 0) {
        setSaveMsg({ ok: true, text: `全部 ${okCount} 个提供商连接成功 · ${detail}` });
      } else {
        setSaveMsg({ ok: okCount > 0, text: `${okCount} 成功 / ${failCount} 失败 · ${detail}` });
      }
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '连接失败';
      setSaveMsg({ ok: false, text: msg });
    } finally {
      setTesting(false);
      setTimeout(() => setSaveMsg(null), 5000);
    }
  };

  const handleClearNonPinned = async () => {
    if (!confirm('确定清除所有非置顶的记忆事实？\n\n此操作会：\n1. 删除所有未置顶的记忆事实\n2. 重置所有群/联系人的记忆提取游标\n\n置顶的记忆会保留。')) return;
    setClearing('facts');
    try {
      await axios.delete('/api/memory/non-pinned');
      setSaveMsg({ ok: true, text: '已清除非置顶记忆' });
    } catch {
      setSaveMsg({ ok: false, text: '清除失败' });
    } finally {
      setClearing(null);
      setTimeout(() => setSaveMsg(null), 3000);
    }
  };

  const handleClearEmbeddings = async () => {
    if (!confirm('确定清除所有 embedding 和向量索引？\n\n此操作会：\n1. 删除所有向量消息（vec_messages）\n2. 删除所有向量索引状态（vec_index_status）\n3. 重置记忆提取游标\n\n需要重新构建向量索引后才能重新提取记忆。')) return;
    setClearing('embeddings');
    try {
      await axios.delete('/api/memory/embeddings');
      setSaveMsg({ ok: true, text: '已清除所有 embedding' });
    } catch {
      setSaveMsg({ ok: false, text: '清除失败' });
    } finally {
      setClearing(null);
      setTimeout(() => setSaveMsg(null), 3000);
    }
  };

  const moveProfile = (index: number, dir: -1 | 1) => {
    const newIndex = index + dir;
    if (newIndex < 0 || newIndex >= profiles.length) return;
    const updated = [...profiles];
    [updated[index], updated[newIndex]] = [updated[newIndex], updated[index]];
    setProfiles(updated);
  };

  const updateProfile = (id: string, updates: Partial<MemLLMProfile>) => {
    setProfiles(prev => prev.map(p => p.id === id ? { ...p, ...updates } : p));
  };

  const removeProfile = (id: string) => {
    if (profiles.length <= 1) return;
    setProfiles(prev => prev.filter(p => p.id !== id));
  };

  if (!loaded) return null;

  return (
    <div className="space-y-3">
      <p className="text-sm text-gray-400 mb-2">
        记忆提炼使用的 LLM 模型。支持配置多个提供商，排在前面的优先使用；
        连续失败时自动 fallback 到后面的提供商（粘性保持 1 小时）。
      </p>

      {/* Provider cards */}
      <div className="space-y-2 mb-4">
        {profiles.map((p, i) => {
          const provInfo = PROVIDERS.find(pr => pr.value === p.provider) ?? PROVIDERS[0];
          const urlPlaceholder = provInfo.defaultURL ? `默认：${provInfo.defaultURL}` : '请输入 Base URL';
          const modelPlaceholder = provInfo.defaultModel ? `默认：${provInfo.defaultModel}` : '请输入模型名';
          return (
            <div key={p.id} className="rounded-xl border border-gray-100 dark:border-white/10 bg-[#fafafa] dark:bg-white/5 p-4 space-y-3">
              <div className="flex items-center gap-2">
                <span className="text-[10px] font-bold text-gray-400 uppercase">#{i + 1}</span>
                <input
                  type="text"
                  value={p.name}
                  onChange={e => updateProfile(p.id, { name: e.target.value })}
                  placeholder={`配置 ${i + 1}`}
                  className="flex-1 text-sm font-semibold border-0 bg-transparent focus:outline-none text-[#1d1d1f] dark:text-gray-200 placeholder-gray-300"
                />
                <div className="flex items-center gap-0.5">
                  <button onClick={() => moveProfile(i, -1)} disabled={i === 0} className="p-1 text-gray-300 hover:text-[#07c160] disabled:opacity-30 transition-colors">
                    <ChevronUp size={14} />
                  </button>
                  <button onClick={() => moveProfile(i, 1)} disabled={i === profiles.length - 1} className="p-1 text-gray-300 hover:text-[#07c160] disabled:opacity-30 transition-colors">
                    <ChevronDown size={14} />
                  </button>
                  {profiles.length > 1 && (
                    <button onClick={() => removeProfile(p.id)} className="p-1 text-gray-300 hover:text-red-400 transition-colors">
                      <X size={14} />
                    </button>
                  )}
                </div>
              </div>

              <div>
                <label className="block text-[10px] font-bold text-gray-400 mb-1 uppercase">提供商</label>
                <select
                  value={p.provider}
                  onChange={e => {
                    const newProv = PROVIDERS.find(pr => pr.value === e.target.value)!;
                    updateProfile(p.id, {
                      provider: e.target.value,
                      base_url: newProv.defaultURL || '',
                      model: newProv.defaultModel || '',
                    });
                  }}
                  className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white dk-input"
                >
                  {PROVIDERS.map(pr => <option key={pr.value} value={pr.value}>{pr.label}</option>)}
                </select>
              </div>

              <div>
                <label className="block text-[10px] font-bold text-gray-400 mb-1 uppercase">API Key</label>
                <input
                  type="password"
                  value={p.api_key === '__HAS_KEY__' ? '' : (p.api_key ?? '')}
                  onChange={e => updateProfile(p.id, { api_key: e.target.value })}
                  placeholder={p.api_key === '__HAS_KEY__' ? '●●●●●● 已保存（留空保留）' : '请输入 API Key'}
                  className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white font-mono dk-input"
                />
              </div>

              <div>
                <label className="block text-[10px] font-bold text-gray-400 mb-1 uppercase">Base URL</label>
                <input
                  type="text"
                  value={p.base_url ?? ''}
                  onChange={e => updateProfile(p.id, { base_url: e.target.value })}
                  placeholder={urlPlaceholder}
                  className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white font-mono dk-input"
                />
              </div>

              <div>
                <label className="block text-[10px] font-bold text-gray-400 mb-1 uppercase">模型</label>
                <input
                  type="text"
                  value={p.model ?? ''}
                  onChange={e => updateProfile(p.id, { model: e.target.value })}
                  placeholder={modelPlaceholder}
                  className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white font-mono dk-input"
                />
              </div>
            </div>
          );
        })}

        <button
          onClick={() => setProfiles(prev => [...prev, newMemLLMProfile(prev.length + 1)])}
          className="w-full flex items-center justify-center gap-1.5 py-2.5 rounded-xl border border-dashed border-gray-200 dark:border-white/10 text-sm text-gray-400 hover:border-[#07c160] hover:text-[#07c160] transition-colors"
        >
          <Plus size={14} />
          添加记忆提炼提供商
        </button>
      </div>

      {/* Save & Test */}
      <div className="flex items-center gap-3">
        <button
          onClick={handleSave}
          disabled={saving}
          className="flex items-center gap-1.5 px-5 py-2.5 bg-[#07c160] text-white text-sm font-bold rounded-xl hover:bg-[#06ad56] disabled:opacity-50 transition-colors"
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
          保存所有配置
        </button>
        <button
          onClick={handleMemTest}
          disabled={testing || saving}
          className="flex items-center gap-1.5 px-4 py-2.5 border border-gray-200 dark:border-white/10 text-gray-600 dark:text-gray-300 text-sm font-bold rounded-xl hover:border-[#07c160] hover:text-[#07c160] disabled:opacity-50 transition-colors"
        >
          {testing ? <Loader2 size={14} className="animate-spin" /> : <AlertCircle size={14} />}
          {testing ? '测试中...' : '测试连接'}
        </button>
        {saveMsg && (
          <span className={`text-sm font-semibold ${saveMsg.ok ? 'text-[#07c160]' : 'text-red-500'}`}>
            {saveMsg.ok ? '✓ ' : '✕ '}{saveMsg.text}
          </span>
        )}
      </div>

      {/* Data management */}
      <div className="pt-2 border-t border-gray-100 dark:border-gray-800 mt-4">
        <p className="text-xs font-semibold text-gray-500 dark:text-gray-400 mb-2">数据管理</p>
        <div className="flex flex-wrap gap-2">
          <button
            onClick={handleClearNonPinned}
            disabled={clearing !== null}
            className="flex items-center gap-1.5 px-3 py-2 text-xs font-bold border border-red-200 dark:border-red-500/30 text-red-600 dark:text-red-400 rounded-xl hover:bg-red-50 dark:hover:bg-red-500/10 disabled:opacity-50 transition-colors"
          >
            {clearing === 'facts' ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
            清除非置顶记忆
          </button>
          <button
            onClick={handleClearEmbeddings}
            disabled={clearing !== null}
            className="flex items-center gap-1.5 px-3 py-2 text-xs font-bold border border-red-200 dark:border-red-500/30 text-red-600 dark:text-red-400 rounded-xl hover:bg-red-50 dark:hover:bg-red-500/10 disabled:opacity-50 transition-colors"
          >
            {clearing === 'embeddings' ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
            清除所有 Embedding
          </button>
        </div>
        <p className="mt-2 text-[10px] text-gray-400 leading-relaxed">
          <strong>清除非置顶记忆</strong>：删除所有未置顶的记忆事实，并重置记忆提取游标（需重新提炼）。<br/>
          <strong>清除所有 Embedding</strong>：删除所有向量消息和向量索引状态（需重新构建向量索引）。
        </p>
      </div>
    </div>
  );
};
