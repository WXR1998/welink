import React, { useState, useCallback, useEffect } from 'react';
import { Plus, Loader2, Check, AlertCircle } from 'lucide-react';
import axios from 'axios';
import { ProfileCard } from './ProfileCard';
import { newProfile, type AIProfileTestResult, type LLMProfile } from './types';
import { streamProfileTestResults } from './streamProfileTestResults';

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
  const [aiDBPath, setAiDBPath] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [testingAll, setTestingAll] = useState(false);
  // per-profile test state
  const [testingId, setTestingId] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<Record<string, AIProfileTestResult>>({});

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
        gemini_client_id?: string; gemini_client_secret?: string;
        ai_analysis_db_path?: string;
      }>('/api/preferences');
      if (r.data.llm_profiles && r.data.llm_profiles.length > 0) {
        setProfiles(r.data.llm_profiles);
        setDefaultProfileId(r.data.default_llm_profile_id || r.data.llm_profiles[0].id);
      }
      setAIQALLMProfiles(r.data.ai_qa_llm_profiles ?? {});
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
    setTestResults(prev => { const next = { ...prev }; delete next[profileId]; return next; });
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      await loadPreferences();
      const r = await axios.post<{ results: AIProfileTestResult[] }>('/api/ai/llm/test', { profile_id: profileId });
      const result = r.data.results?.[0];
      if (!result) throw new Error('未收到测试结果');
      setTestResults(prev => ({ ...prev, [profileId]: result }));
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '连接失败';
      const profile = profiles.find(item => item.id === profileId);
      setTestResults(prev => ({
        ...prev,
        [profileId]: {
          profile_id: profileId,
          name: profile?.name || 'AI 配置',
          provider: profile?.provider || '',
          model: profile?.model || '',
          ok: false,
          latency_ms: 0,
          protocols: [],
          error: msg,
        },
      }));
    } finally {
      setTestingId(null);
    }
  };

  const handleTestAll = async () => {
    setTestingAll(true);
    setSaveMsg(null);
    setTestResults({});
    try {
      await axios.put('/api/preferences/llm', await buildPayload());
      await loadPreferences();
      let resultCount = 0;
      let hasSuccess = false;
      await streamProfileTestResults('/api/ai/llm/test', { profile_id: '__all__' }, result => {
        resultCount++;
        hasSuccess = hasSuccess || result.ok;
        setTestResults(previous => ({ ...previous, [result.profile_id]: result }));
      });
      setSaveMsg({ ok: hasSuccess, text: `已完成 ${resultCount} 个配置测试，详见各配置卡片` });
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : '连接失败';
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
            testResult={testResults[p.id] ?? null}
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
