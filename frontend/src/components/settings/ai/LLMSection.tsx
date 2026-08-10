import React, { useState, useCallback, useEffect } from 'react';
import { Plus, Loader2, Check, AlertCircle, Zap } from 'lucide-react';
import axios from 'axios';
import { ProfileCard } from './ProfileCard';
import { genId, newProfile, type LLMProfile } from './types';

interface AIQALLMProfiles {
  query_decomposition?: string;
  query_expansion?: string;
  final_answer?: string;
}

const aiQASteps: { key: keyof AIQALLMProfiles; label: string }[] = [
  { key: 'query_decomposition', label: '问题分解' },
  { key: 'query_expansion', label: '查询扩展' },
  { key: 'final_answer', label: '最终回答' },
];

export const LLMSection: React.FC = () => {
  const [profiles, setProfiles] = useState<LLMProfile[]>([newProfile(1)]);
  const [defaultProfileId, setDefaultProfileId] = useState('');
  const [aiQALLMProfiles, setAIQALLMProfiles] = useState<AIQALLMProfiles>({});
  const [openAIFastMode, setOpenAIFastMode] = useState(false);
  const [aiDBPath, setAiDBPath] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [testingAll, setTestingAll] = useState(false);
  // per-profile test state
  const [testingId, setTestingId] = useState<string | null>(null);
  const [testMsgs, setTestMsgs] = useState<Record<string, { ok: boolean; text: string }>>({});

  // Gemini OAuth（全局）
  const [geminiClientID, setGeminiClientID] = useState('');
  const [geminiClientSecret, setGeminiClientSecret] = useState('');
  const [geminiAuthorized, setGeminiAuthorized] = useState(false);
  const [geminiAuthBusy, setGeminiAuthBusy] = useState(false);
  const pollRef = React.useRef<ReturnType<typeof setInterval> | null>(null);

  const checkGeminiStatus = async () => {
    try {
      const r = await axios.get<{ authorized: boolean }>('/api/auth/gemini/status');
      setGeminiAuthorized(r.data.authorized);
      return r.data.authorized;
    } catch { return false; }
  };

  // 从后端加载配置（初始化 + 保存后刷新）
  const loadPreferences = useCallback(async () => {
    try {
      const r = await axios.get<{
        llm_profiles?: LLMProfile[];
        default_llm_profile_id?: string;
        ai_qa_llm_profiles?: AIQALLMProfiles;
        openai_fast_mode?: boolean;
        gemini_client_id?: string; gemini_client_secret?: string;
        ai_analysis_db_path?: string;
      }>('/api/preferences');
      if (r.data.llm_profiles && r.data.llm_profiles.length > 0) {
        setProfiles(r.data.llm_profiles);
        setDefaultProfileId(r.data.default_llm_profile_id || r.data.llm_profiles[0].id);
      }
      setAIQALLMProfiles(r.data.ai_qa_llm_profiles ?? {});
      setOpenAIFastMode(r.data.openai_fast_mode ?? false);
      setGeminiClientID(r.data.gemini_client_id ?? '');
      setGeminiClientSecret(r.data.gemini_client_secret ?? '');
      setAiDBPath(r.data.ai_analysis_db_path ?? '');
    } catch {} finally { setLoaded(true); }
  }, []);

  useEffect(() => {
    loadPreferences();
    checkGeminiStatus();
  }, [loadPreferences]);

  // 卸载时清掉 Gemini OAuth 轮询，避免 orphan interval 持续打后端
  useEffect(() => {
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, []);

  // save 时实时拉最新 prefs 再 merge，避免覆盖别的 tab 刚保存的字段
  // （embedding / mem_llm 等非本 tab 字段从 fresh.data 带过来，本 tab 字段用本地最新值）
  const buildPayload = async () => {
    let fresh: Record<string, unknown> = {};
    try {
      const r = await axios.get<Record<string, unknown>>('/api/preferences');
      fresh = r.data;
    } catch { /* 拿不到就只发本 tab 的字段，等同于旧行为 */ }
    return {
      ...fresh,
      llm_profiles: profiles,
      default_llm_profile_id: defaultProfileId || profiles[0]?.id || '',
      ai_qa_llm_profiles: aiQALLMProfiles,
      openai_fast_mode: openAIFastMode,
      gemini_client_id: geminiClientID,
      gemini_client_secret: geminiClientSecret,
      ai_analysis_db_path: aiDBPath,
    };
  };

  const handleSave = async () => {
    setSaving(true);
    setSaveMsg(null);
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      await loadPreferences(); // 重新从后端加载，确保 __HAS_KEY__ 标记正确
      setSaveMsg({ ok: true, text: '已保存' });
    } catch {
      setSaveMsg({ ok: false, text: '保存失败' });
    } finally {
      setSaving(false);
      setTimeout(() => setSaveMsg(null), 3000);
    }
  };

  const handleSaveAndTest = async (profileId: string) => {
    setTestingId(profileId);
    setTestMsgs(prev => { const n = { ...prev }; delete n[profileId]; return n; });
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      await loadPreferences();
      const r = await axios.post<{ ok: boolean; provider: string; model: string; latency_ms: number; tokens_per_second: number }>('/api/ai/llm/test', { profile_id: profileId });
      const parts = [`${r.data.provider} · ${r.data.model}`];
      if (r.data.latency_ms > 0) parts.push(`${r.data.latency_ms}ms`);
      if (r.data.tokens_per_second > 0) parts.push(`${r.data.tokens_per_second.toFixed(1)} tok/s`);
      setTestMsgs(prev => ({ ...prev, [profileId]: { ok: true, text: parts.join(' · ') } }));
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '连接失败';
      setTestMsgs(prev => ({ ...prev, [profileId]: { ok: false, text: msg } }));
    } finally {
      setTestingId(null);
      setTimeout(() => setTestMsgs(prev => { const n = { ...prev }; delete n[profileId]; return n; }), 5000);
    }
  };

  const handleTestAll = async () => {
    setTestingAll(true);
    setSaveMsg(null);
    setTestMsgs({});
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      await loadPreferences();
      const r = await axios.post<{ results: { profile_id: string; name: string; provider: string; model: string; ok: boolean; latency_ms: number; tokens_per_second: number; error?: string }[] }>('/api/ai/llm/test', { profile_id: '__all__' });
      const results = r.data.results ?? [];
      const newMsgs: Record<string, { ok: boolean; text: string }> = {};
      const okCount = results.filter(r => r.ok).length;
      const failCount = results.length - okCount;
      for (const res of results) {
        if (res.ok) {
          const parts = [`${res.provider} · ${res.model}`];
          if (res.latency_ms > 0) parts.push(`${res.latency_ms}ms`);
          if (res.tokens_per_second > 0) parts.push(`${res.tokens_per_second.toFixed(1)} tok/s`);
          newMsgs[res.profile_id] = { ok: true, text: parts.join(' · ') };
        } else {
          newMsgs[res.profile_id] = { ok: false, text: res.error || '连接失败' };
        }
      }
      setTestMsgs(newMsgs);
      if (failCount === 0) {
        setSaveMsg({ ok: true, text: `全部 ${okCount} 个配置连接成功` });
      } else {
        setSaveMsg({ ok: okCount > 0, text: `${okCount} 成功 / ${failCount} 失败` });
      }
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '连接失败';
      setSaveMsg({ ok: false, text: msg });
    } finally {
      setTestingAll(false);
      setTimeout(() => setSaveMsg(null), 5000);
    }
  };

  const handleGeminiAuth = async () => {
    if (!geminiClientID || !geminiClientSecret) {
      setSaveMsg({ ok: false, text: '请先填写 Client ID 和 Client Secret' });
      setTimeout(() => setSaveMsg(null), 3000);
      return;
    }
    await axios.put('/api/preferences/llm', await buildPayload()).catch(() => {});
    try {
      const r = await axios.get<{ url: string }>('/api/auth/gemini/url');
      window.open(r.data.url, '_blank');
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '获取授权地址失败';
      setSaveMsg({ ok: false, text: msg });
      setTimeout(() => setSaveMsg(null), 3000);
      return;
    }
    setGeminiAuthBusy(true);
    let attempts = 0;
    pollRef.current = setInterval(async () => {
      attempts++;
      const authed = await checkGeminiStatus();
      if (authed || attempts > 30) {
        clearInterval(pollRef.current!);
        setGeminiAuthBusy(false);
        if (authed) setSaveMsg({ ok: true, text: 'Google 授权成功！' });
        setTimeout(() => setSaveMsg(null), 3000);
      }
    }, 2000);
  };

  const handleGeminiRevoke = async () => {
    await axios.delete('/api/auth/gemini').catch(() => {});
    setGeminiAuthorized(false);
  };

  if (!loaded) return null;

  return (
    <div className="space-y-3">
        <div className="flex items-center justify-between gap-3 rounded-xl border border-gray-100 p-4 dk-border">
          <div className="min-w-0">
            <div className="flex items-center gap-2 text-sm font-medium text-[#1d1d1f] dark:text-gray-200">
              <Zap size={15} className="text-amber-500" />
              OpenAI Fast 模式
            </div>
            <p className="mt-1 text-[11px] text-gray-400">对原生 OpenAI 配置使用低延迟服务档位；会提高按 token 价格，其他供应商不受影响。</p>
          </div>
          <button
            type="button"
            onClick={() => setOpenAIFastMode(value => !value)}
            className={`relative inline-flex h-5 w-9 flex-shrink-0 items-center rounded-full transition-colors ${openAIFastMode ? 'bg-[#07c160]' : 'bg-gray-200 dark:bg-white/20'}`}
            title="切换 OpenAI Fast 模式"
            aria-pressed={openAIFastMode}
          >
            <span className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform ${openAIFastMode ? 'translate-x-[18px]' : 'translate-x-0.5'}`} />
          </button>
        </div>

        {profiles.map((p, i) => (
          <ProfileCard
            key={p.id}
            profile={p}
            index={i}
            total={profiles.length}
            geminiAuthorized={geminiAuthorized}
            geminiClientID={geminiClientID}
            geminiClientSecret={geminiClientSecret}
            onGeminiClientIDChange={setGeminiClientID}
            onGeminiClientSecretChange={setGeminiClientSecret}
            onGeminiAuth={handleGeminiAuth}
            onGeminiRevoke={handleGeminiRevoke}
            geminiAuthBusy={geminiAuthBusy}
            onChange={updated => setProfiles(prev => prev.map(x => x.id === updated.id ? updated : x))}
            onDelete={() => setProfiles(prev => {
              const next = prev.filter(x => x.id !== p.id);
              if (p.id === defaultProfileId) setDefaultProfileId(next[0]?.id ?? '');
              setAIQALLMProfiles(current => {
                const updated = { ...current };
                for (const step of aiQASteps) {
                  if (updated[step.key] === p.id) updated[step.key] = '';
                }
                return updated;
              });
              return next;
            })}
            onSaveAndTest={handleSaveAndTest}
            testing={testingId === p.id}
            testMsg={testMsgs[p.id] ?? null}
          />
        ))}

        {profiles.length > 1 && (
          <label className="block text-xs text-gray-500 dark:text-gray-400">
            默认 AI 配置
            <select value={defaultProfileId} onChange={e => setDefaultProfileId(e.target.value)} className="mt-1 w-full text-sm border border-gray-200 rounded-lg px-3 py-2 bg-white dk-input">
              {profiles.map((p, i) => <option key={p.id} value={p.id}>{p.name || `配置 ${i + 1}`}</option>)}
            </select>
          </label>
        )}

        <div className="rounded-xl border border-gray-100 p-4 space-y-3 dk-border">
          <div>
            <p className="text-xs font-bold text-gray-500 dark:text-gray-400 uppercase tracking-wide">问答步骤模型</p>
            <p className="mt-1 text-[11px] text-gray-400">留空时跟随 AI 问答页面当前选择的模型。</p>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {aiQASteps.map(step => (
              <label key={step.key} className="block text-xs text-gray-500 dark:text-gray-400">
                {step.label}
                <select
                  value={aiQALLMProfiles[step.key] ?? ''}
                  onChange={e => setAIQALLMProfiles(current => ({ ...current, [step.key]: e.target.value }))}
                  className="mt-1 w-full text-sm border border-gray-200 rounded-lg px-3 py-2 bg-white dk-input"
                >
                  <option value="">跟随当前问答模型</option>
                  {profiles.map((p, i) => <option key={p.id} value={p.id}>{p.name || `配置 ${i + 1}`}</option>)}
                </select>
              </label>
            ))}
          </div>
        </div>

        {/* 添加配置 */}
        <button
          onClick={() => setProfiles(prev => [...prev, newProfile(prev.length + 1)])}
          className="w-full flex items-center justify-center gap-1.5 py-2.5 rounded-xl border border-dashed border-gray-200 dark:border-white/10 text-sm text-gray-400 hover:border-[#07c160] hover:text-[#07c160] transition-colors"
        >
          <Plus size={14} />
          添加 AI 配置
        </button>

        {/* 分析历史数据库路径 */}
        <div className="bg-white rounded-xl border border-gray-100 p-4 space-y-2 dk-card dk-border">
          <label className="block text-xs font-bold text-gray-500 dark:text-gray-400 uppercase tracking-wide">
            分析历史数据库路径 <span className="text-gray-400 font-normal normal-case">（留空使用默认）</span>
          </label>
          <input
            type="text"
            value={aiDBPath}
            onChange={e => setAiDBPath(e.target.value)}
            placeholder="留空则与配置文件同目录，如 /data/ai_analysis.db"
            className="w-full text-sm border border-gray-200 rounded-lg px-3 py-2 focus:outline-none focus:border-[#07c160] bg-[#f8f9fb] font-mono dk-input"
          />
          <p className="text-[10px] text-gray-400">Docker 建议设为挂载目录下的路径，确保容器重启后分析记录不丢失。</p>
        </div>

        {/* 保存所有 + 测试所有 */}
        <div className="flex items-center gap-3 flex-wrap">
          <button
            onClick={handleSave}
            disabled={saving}
            className="flex items-center gap-1.5 px-5 py-2.5 bg-[#07c160] text-white text-sm font-bold rounded-xl hover:bg-[#06ad56] disabled:opacity-50 transition-colors"
          >
            {saving ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
            保存所有配置
          </button>
          <button
            onClick={handleTestAll}
            disabled={testingAll || saving || profiles.length === 0}
            className="flex items-center gap-1.5 px-4 py-2.5 border border-gray-200 dark:border-white/10 text-gray-600 dark:text-gray-300 text-sm font-bold rounded-xl hover:border-[#07c160] hover:text-[#07c160] disabled:opacity-50 transition-colors"
          >
            {testingAll ? <Loader2 size={14} className="animate-spin" /> : <AlertCircle size={14} />}
            {testingAll ? '测试中...' : '测试所有配置'}
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
