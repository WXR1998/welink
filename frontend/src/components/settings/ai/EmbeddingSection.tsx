import React, { useState, useEffect } from 'react';
import { Loader2, AlertCircle, Check, Plus, ChevronUp, ChevronDown, X } from 'lucide-react';
import axios from 'axios';
import { genId, newEmbeddingProfile, type EmbeddingProfile } from './types';

const EMBEDDING_PROVIDERS = [
  { value: 'ollama',  label: 'Ollama（本地，免费）', defaultURL: 'http://localhost:11434', defaultModel: 'nomic-embed-text', defaultDims: 768, needsKey: false },
  { value: 'openai',  label: 'OpenAI', defaultURL: 'https://api.openai.com/v1', defaultModel: 'text-embedding-3-small', defaultDims: 1536, needsKey: true },
  { value: 'jina',    label: 'Jina AI', defaultURL: 'https://api.jina.ai/v1', defaultModel: 'jina-embeddings-v3', defaultDims: 1024, needsKey: true },
  { value: 'custom',  label: '自定义（OpenAI 兼容）', defaultURL: '', defaultModel: '', defaultDims: 0, needsKey: true },
] as const;

type EmbeddingProviderValue = typeof EMBEDDING_PROVIDERS[number]['value'];

export const EmbeddingSection: React.FC = () => {
  const [profiles, setProfiles] = useState<EmbeddingProfile[]>([]);
  const [cacheMaxKeys, setCacheMaxKeys] = useState(3);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    axios.get<Record<string, unknown>>('/api/preferences').then(r => {
      const eps = (r.data.embedding_profiles as EmbeddingProfile[] | undefined);
      if (eps && eps.length > 0) {
        setProfiles(eps);
      } else {
        // Migrate from single config
        setProfiles([{
          id: genId(),
          name: '默认',
          provider: (r.data.embedding_provider as string) || 'ollama',
          api_key: (r.data.embedding_api_key as string) || '',
          base_url: (r.data.embedding_base_url as string) || '',
          model: (r.data.embedding_model as string) || '',
          dims: (r.data.embedding_dims as number) || 768,
        }]);
      }
      setCacheMaxKeys((r.data.vec_cache_max_keys as number) || 3);
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
      embedding_profiles: profiles,
      vec_cache_max_keys: cacheMaxKeys,
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
      const r = await axios.post<{ results: { provider: string; model: string; ok: boolean; latency_ms: number; error?: string }[] }>('/api/ai/vec/test-embedding');
      const results = r.data.results ?? [];
      const okCount = results.filter(r => r.ok).length;
      const failCount = results.length - okCount;
      const detail = results.map(r => r.ok ? `${r.provider}: ${r.latency_ms}ms` : `${r.provider}: ${r.error ?? '失败'}`).join('；');
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

  const moveProfile = (index: number, dir: -1 | 1) => {
    const newIndex = index + dir;
    if (newIndex < 0 || newIndex >= profiles.length) return;
    const updated = [...profiles];
    [updated[index], updated[newIndex]] = [updated[newIndex], updated[index]];
    setProfiles(updated);
  };

  const updateProfile = (id: string, updates: Partial<EmbeddingProfile>) => {
    setProfiles(prev => prev.map(p => p.id === id ? { ...p, ...updates } : p));
  };

  const removeProfile = (id: string) => {
    if (profiles.length <= 1) return;
    setProfiles(prev => prev.filter(p => p.id !== id));
  };

  if (!loaded) return null;

  return (
    <div>
      <p className="text-sm text-gray-400 mb-4">
        用于混合检索模式的语义向量化。支持配置多个提供商，排在前面的优先使用；
        连续失败时自动 fallback 到后面的提供商（粘性保持 1 小时）。
      </p>

      {/* Provider cards */}
      <div className="space-y-2 mb-4">
        {profiles.map((p, i) => {
          const provInfo = EMBEDDING_PROVIDERS.find(pr => pr.value === p.provider) ?? EMBEDDING_PROVIDERS[0];
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
                    const newProv = EMBEDDING_PROVIDERS.find(pr => pr.value === e.target.value)!;
                    updateProfile(p.id, {
                      provider: e.target.value,
                      base_url: newProv.defaultURL || '',
                      model: newProv.defaultModel || '',
                      dims: newProv.defaultDims || 0,
                    });
                  }}
                  className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white dk-input"
                >
                  {EMBEDDING_PROVIDERS.map(pr => <option key={pr.value} value={pr.value}>{pr.label}</option>)}
                </select>
              </div>

              {provInfo.needsKey && (
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

              <div className="grid grid-cols-2 gap-3">
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
                <div>
                  <label className="block text-[10px] font-bold text-gray-400 mb-1 uppercase">维度</label>
                  <input
                    type="number"
                    value={p.dims ?? 0}
                    onChange={e => updateProfile(p.id, { dims: parseInt(e.target.value) || 0 })}
                    placeholder="0 = 默认"
                    className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-white font-mono dk-input"
                  />
                </div>
              </div>
            </div>
          );
        })}

        <button
          onClick={() => setProfiles(prev => [...prev, newEmbeddingProfile(prev.length + 1)])}
          className="w-full flex items-center justify-center gap-1.5 py-2.5 rounded-xl border border-dashed border-gray-200 dark:border-white/10 text-sm text-gray-400 hover:border-[#07c160] hover:text-[#07c160] transition-colors"
        >
          <Plus size={14} />
          添加 Embedding 提供商
        </button>
      </div>

      {/* Cache settings */}
      <div className="bg-white rounded-2xl border border-gray-100 p-5 space-y-4 dk-card dk-border">
        <div>
          <label className="block text-xs font-bold text-gray-500 dark:text-gray-400 mb-1.5 uppercase tracking-wide">
            向量缓存联系人数 <span className="text-gray-400 font-normal normal-case">（内存中最多缓存几个联系人的 Embedding，默认 3）</span>
          </label>
          <div className="flex items-center gap-3">
            <input
              type="number"
              min={1}
              max={50}
              value={cacheMaxKeys}
              onChange={e => setCacheMaxKeys(Math.max(1, Math.min(50, parseInt(e.target.value) || 3)))}
              className="w-24 text-sm border border-gray-200 rounded-xl px-3 py-2.5 focus:outline-none focus:border-[#07c160] bg-[#f8f9fb] dk-input"
            />
            <span className="text-xs text-gray-400 leading-relaxed">
              每个联系人约占 <span className="font-semibold">消息数 × 768维 × 4B</span>（nomic-embed-text），20万条约 600MB。内存充裕可适当调大。
            </span>
          </div>
        </div>

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
      </div>
    </div>
  );
};
