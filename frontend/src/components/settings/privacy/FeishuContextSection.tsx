import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { Bot, ChevronDown, RefreshCw, Trash2 } from 'lucide-react';

interface FeishuSession {
  key: string;
  chat_id?: string;
  user_id?: string;
  chat_name?: string;
  user_name?: string;
  created_at?: string;
  last_active: string;
  msg_count: number;
  chars: number;
  compressed?: boolean;
  entity?: string;
  content_preview?: string;
}

/**
 * 飞书机器人会话上下文管理。
 *
 * 展示每个「飞书群 × 提问人」的独立上下文，支持手动查看和删除。
 * 后端经 FEISHU_BOT_MANAGE_URL 代理到飞书机器人内部管理端点。
 */
export const FeishuContextSection: React.FC = () => {
  const [sessions, setSessions] = useState<FeishuSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [refreshTick, setRefreshTick] = useState(0);

  const load = useCallback(() => {
    setLoading(true);
    axios
      .get<{ sessions: FeishuSession[] }>('/api/feishu/sessions')
      .then((r) => {
        setSessions(r.data.sessions ?? []);
        setUnavailable(false);
      })
      .catch(() => {
        setSessions([]);
        setUnavailable(true);
      })
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load();
  }, [load, refreshTick]);

  const remove = async (key: string) => {
    if (!window.confirm('确定删除该群/该人的上下文吗？')) return;
    try {
      await axios.delete('/api/feishu/sessions', { params: { key } });
      setSessions((prev) => prev.filter((s) => s.key !== key));
    } catch {
      window.alert('删除失败');
    }
  };

  const fmt = (v?: string) => {
    if (!v) return '—';
    const d = new Date(v);
    return Number.isNaN(d.getTime()) ? v : d.toLocaleString('zh-CN', { hour12: false });
  };

  const short = (v?: string) => {
    if (!v) return '';
    return v.length > 12 ? `${v.slice(0, 6)}…${v.slice(-4)}` : v;
  };
  const chatLabel = (s: FeishuSession) =>
    s.chat_name ? `群 ${s.chat_name}` : s.chat_id ? `群 ${short(s.chat_id)}` : '单聊';
  const userLabel = (s: FeishuSession) =>
    s.user_name ? `人 ${s.user_name}` : s.user_id ? `人 ${short(s.user_id)}` : '未知用户';

  return (
    <section className="mb-8" data-section-id="feishu-context" data-settings-tags="飞书 上下文 会话 管理 feishu context session delete">
      <div className="flex items-center gap-2 mb-3">
        <Bot size={18} className="text-[#07c160]" />
        <h3 className="text-base font-bold text-[#1d1d1f] dk-text">飞书机器人上下文管理</h3>
      </div>
      <p className="text-sm text-gray-400 mb-4">
        查看和删除「某个人在某个飞书群」的当前独立上下文。删除后该会话会重新开始计时。
      </p>

      <div className="flex items-center gap-2 mb-3">
        <button
          onClick={() => setRefreshTick((t) => t + 1)}
          className="inline-flex items-center gap-1.5 text-xs font-semibold px-3 py-1.5 bg-white border border-gray-200 text-gray-600 rounded-lg hover:border-[#07c160] transition-colors dk-card dk-border"
        >
          <RefreshCw size={14} /> 刷新
        </button>
        {loading && <span className="text-xs text-gray-400">加载中…</span>}
      </div>

      {unavailable && (
        <div className="text-sm text-gray-400 bg-white rounded-2xl border border-gray-100 p-6 dk-card dk-border">
          飞书机器人未启用会话管理（后端未配置 FEISHU_BOT_MANAGE_URL，或机器人管理服务未启动）。
        </div>
      )}

      {!unavailable && sessions.length === 0 && !loading && (
        <div className="text-sm text-gray-400 bg-white rounded-2xl border border-gray-100 p-6 dk-card dk-border">
          当前没有活跃的飞书会话上下文。
        </div>
      )}

      <div className="space-y-3">
        {sessions.map((s) => (
          <div key={s.key} className="bg-white rounded-2xl border border-gray-100 p-4 dk-card dk-border">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="font-bold text-[#1d1d1f] dk-text text-sm truncate">{chatLabel(s)} · {userLabel(s)}</div>
                <div className="text-xs text-gray-400 mt-1 break-all">{s.key}</div>
              </div>
              <button
                onClick={() => remove(s.key)}
                className="shrink-0 inline-flex items-center gap-1 text-xs px-2.5 py-1.5 rounded-lg text-red-600 hover:bg-red-50 transition-colors"
                title="删除该上下文"
              >
                <Trash2 size={14} /> 删除
              </button>
            </div>

            <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 mt-3">
              <div className="text-xs">
                <div className="text-gray-400">上下文始于</div>
                <div className="font-medium text-[#1d1d1f] dk-text">{fmt(s.created_at)}</div>
              </div>
              <div className="text-xs">
                <div className="text-gray-400">最后活跃</div>
                <div className="font-medium text-[#1d1d1f] dk-text">{fmt(s.last_active)}</div>
              </div>
              <div className="text-xs">
                <div className="text-gray-400">问答条数</div>
                <div className="font-medium text-[#1d1d1f] dk-text">{s.msg_count}</div>
              </div>
              <div className="text-xs">
                <div className="text-gray-400">状态</div>
                <div className="font-medium text-[#1d1d1f] dk-text">{s.compressed ? '已压缩' : '正常'}</div>
              </div>
            </div>

            {s.entity && (
              <div className="text-xs text-gray-400 mt-2">最近实体：{s.entity}</div>
            )}

            <details className="mt-3 group">
              <summary className="inline-flex items-center gap-1 text-xs font-semibold text-[#07c160] cursor-pointer select-none">
                <ChevronDown size={14} className="transition-transform group-open:rotate-180" />
                {s.content_preview ? '查看上下文内容' : '上下文为空'}
              </summary>
              {s.content_preview && (
                <pre className="mt-2 text-xs whitespace-pre-wrap break-words text-gray-600 max-h-96 overflow-y-auto bg-gray-50 rounded-lg p-3 dk-card dk-border">
                  {s.content_preview}
                </pre>
              )}
            </details>
          </div>
        ))}
      </div>
    </section>
  );
};
