import React, { useState, useEffect } from 'react';
import { Loader2, AlertCircle, Check, Plus, X } from 'lucide-react';
import axios from 'axios';
import { genId, type RerankProfile } from './types';

const RERANK_PROVIDERS = [
  { value: 'jina',         label: 'Jina AI',           defaultURL: 'https://api.jina.ai/v1',         defaultModel: 'jina-reranker-v2-base-multilingual', needsKey: true },
  { value: 'cohere',       label: 'Cohere',            defaultURL: 'https://api.cohere.ai/v1',       defaultModel: 'rerank-multilingual-v3.0',           needsKey: true },
  { value: 'siliconflow',  label: '硅基流动 SiliconFlow', defaultURL: 'https://api.siliconflow.cn/v1', defaultModel: 'BAAI/bge-reranker-v2-m3',            needsKey: true },
  { value: 'custom',       label: '自定义（/rerank 兼容）', defaultURL: '',                              defaultModel: '',                                   needsKey: true },
] as const;

export const RerankSection: React.FC = () => {
  const [profiles, setProfiles] = useState<RerankProfile[]>([]);
  const [defaultProfileId, setDefaultProfileId] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    axios.get<Record<string, unknown>>('/api/preferences').then(r => {
      const rps = (r.data.rerank_profiles as RerankProfile[] | undefined);
      if (rps && rps.length > 0) {
        setProfiles(rps);
        setDefaultProfileId((r.data.default_rerank_profile_id as string) || rps[0].id);
      } else {
        setProfiles([]);
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
      rerank_profiles: profiles,
      default_rerank_profile_id: defaultProfileId || profiles[0]?.id || '',
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

  const handleTest = async () => {
    setTesting(true);
    setSaveMsg(null);
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      const r = await axios.post<{ results: { provider: string; model: string; ok: boolean; latency_ms: number; error?: string }[] }>('/api/ai/rerank/test');
      const results = r.data.results ?? [];
      const detail = results.map(r => r.ok ? `${r.provider}: ${r.latency_ms}ms` : `${r.provider}: ${r.error ?? '失败'}`).join('；');
      setSaveMsg({ ok: results[0]?.ok === true, text: `${results[0]?.ok ? '当前配置连接成功' : '当前配置连接失败'}${detail ? ` · ${detail}` : ''}` });
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '连接失败';
      setSaveMsg({ ok: false, text: msg });
    } finally {
      setTesting(false);
      setTimeout(() => setSaveMsg(null), 5000);
    }
  };

  const updateProfile = (id: string, updates: Partial<RerankProfile>) => {
    setProfiles(prev => prev.map(p => p.id === id ? { ...p, ...updates } : p));
  };

  const removeProfile = (id: string) => {
    const updated = profiles.filter(p => p.id !== id);
    setProfiles(updated);
    if (defaultProfileId === id) setDefaultProfileId(updated[0]?.id ?? '');
  };

  const addProfile = () => {
    setProfiles(prev => [...prev, { id: genId(), name: `Rerank ${prev.length + 1}`, provider: 'jina', api_key: '', base_url: '', model: '' }]);
  };

  if (!loaded) return null;

  return (
    <div>
      <p className="text-sm text-gray-400 mb-4">
        用于对向量检索召回的候选做 cross-encoder 精排。请选择一个当前使用的配置；调用失败不会切换其他配置。留空则不启用。
      </p>

      {/* Provider cards */}
      <div className="space-y-2 mb-4">
        {profiles.map((p) => {
          const provInfo = RERANK_PROVIDERS.find(pr => pr.value === p.provider) ?? RERANK_PROVIDERS[0];
          const urlPlaceholder = provInfo.defaultURL ? `默认：${provInfo.defaultURL}` : '请输入 Base URL';
          const modelPlaceholder = provInfo.defaultModel ? `默认：${provInfo.defaultModel}` : '请输入模型名';
          return (
            <div key={p.id} className="bg-white rounded-2xl border border-gray-100 p-4 space-y-3 dk-card dk-border">
              <div className="flex items-center justify-between">
                <input
                  type="text"
                  value={p.name}
                  onChange={e => updateProfile(p.id, { name: e.target.value })}
                  className="flex-1 text-sm font-bold border-b border-transparent hover:border-gray-200 focus:outline-none focus:border-[#07c160] bg-transparent"
                  placeholder="配置名称"
                />
                <button
                  onClick={() => removeProfile(p.id)}
                  className="ml-2 p-1 text-gray-300 hover:text-red-400 transition-colors"
                >
                  <X size={16} />
                </button>
              </div>

              <div>
                <label className="block text-[10px] font-bold text-gray-400 mb-1 uppercase">提供商</label>
                <select
                  value={p.provider}
                  onChange={e => updateProfile(p.id, { provider: e.target.value })}
                  className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white dk-input"
                >
                  {RERANK_PROVIDERS.map(pr => (
                    <option key={pr.value} value={pr.value}>{pr.label}</option>
                  ))}
                </select>
              </div>

              {provInfo.needsKey && (
                <div>
                  <label className="block text-[10px] font-bold text-gray-400 mb-1 uppercase">API Key</label>
                  <input
                    type="password"
                    value={p.api_key ?? ''}
                    onChange={e => updateProfile(p.id, { api_key: e.target.value })}
                    placeholder={p.api_key === '__HAS_KEY__' ? '●●●●●● 已保存（留空保留）' : '请输入 API Key'}
                    className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white font-mono dk-input"
                  />
                </div>
              )}

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
          onClick={addProfile}
          className="w-full flex items-center justify-center gap-1.5 py-2.5 rounded-xl border border-dashed border-gray-200 dark:border-white/10 text-sm text-gray-400 hover:border-[#07c160] hover:text-[#07c160] transition-colors"
        >
          <Plus size={14} />
          添加 Rerank 提供商
        </button>
      </div>

      {profiles.length > 1 && (
        <label className="block text-xs text-gray-500 dark:text-gray-400 mb-4">
          当前 Rerank 配置
          <select value={defaultProfileId} onChange={e => setDefaultProfileId(e.target.value)} className="mt-1 w-full text-sm border border-gray-200 rounded-lg px-3 py-2 bg-white dk-input">
            {profiles.map((p, i) => <option key={p.id} value={p.id}>{p.name || `配置 ${i + 1}`}</option>)}
          </select>
        </label>
      )}

      {/* Save / Test buttons */}
      <div className="flex items-center gap-3 pt-1">
        <button
          onClick={handleSave}
          disabled={saving}
          className="flex items-center gap-1.5 px-5 py-2.5 bg-[#07c160] text-white text-sm font-bold rounded-xl hover:bg-[#06ad56] disabled:opacity-50 transition-colors"
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
          保存
        </button>
        <button
          onClick={handleTest}
          disabled={testing || saving || profiles.length === 0}
          className="flex items-center gap-1.5 px-4 py-2.5 border border-gray-200 dark:border-white/10 text-gray-600 dark:text-gray-300 text-sm font-bold rounded-xl hover:border-[#07c160] hover:text-[#07c160] disabled:opacity-50 transition-colors"
        >
          {testing ? <Loader2 size={14} className="animate-spin" /> : <AlertCircle size={14} />}
          测试连接
        </button>
        {saveMsg && (
          <span className={`text-sm font-semibold ${saveMsg.ok ? 'text-[#07c160]' : 'text-red-500'}`}>
            {saveMsg.ok ? '✓ ' : '✕ '}{saveMsg.text}
          </span>
        )}
      </div>
    </div>
  );
};
