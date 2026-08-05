import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { Bot, X } from 'lucide-react';
import type { ContactStats, GroupInfo } from '../../../types';

interface ScopeData {
  feishu_group_scope: Record<string, string[]>;
  feishu_bot_chats: Record<string, string>;
}

/**
 * 飞书群问答白名单设置。
 *
 * 每个飞书群（chat_id）可选择允许问答访问的微信群/联系人。
 * 白名单只做白名单：群（group:xxx）与私聊（contact:xxx）一视同仁。
 * 未配置的飞书群默认放行（兼容现状）。
 */
export const FeishuScopeSection: React.FC<{
  allContacts?: ContactStats[];
  allGroups?: GroupInfo[];
}> = ({ allContacts = [], allGroups = [] }) => {
  const [chatNames, setChatNames] = useState<Record<string, string>>({});
  const [scope, setScope] = useState<Record<string, string[]>>({});
  const [drafts, setDrafts] = useState<Record<string, string[]>>({});
  const [dirtyKeys, setDirtyKeys] = useState<Set<string>>(new Set());

  useEffect(() => {
    axios
      .get<ScopeData>('/api/preferences/feishu-scope')
      .then((r) => {
        setScope(r.data.feishu_group_scope ?? {});
        setDrafts(r.data.feishu_group_scope ?? {});
        setChatNames(r.data.feishu_bot_chats ?? {});
      })
      .catch(() => {});
  }, []);

  // 合并：上报的飞书群 + 已配置白名单的 chat_id（去重保序）
  const chatKeys = Array.from(new Set([...Object.keys(scope), ...Object.keys(drafts)]));

  const groupLabel = (id: string): string => {
    const g = allGroups.find((x) => x.username === id);
    return g ? g.name : id;
  };
  const contactLabel = (id: string): string => {
    const c = allContacts.find((x) => x.username === id || x.remark === id || x.nickname === id);
    return c ? (c.remark || c.nickname || id) : id;
  };
  const labelForKey = (key: string): string => {
    if (key.startsWith('group:')) return '群·' + groupLabel(key.slice(6));
    if (key.startsWith('contact:')) return '联系人·' + contactLabel(key.slice(8));
    return key;
  };

  const toggleKey = (chatID: string, key: string) => {
    setDrafts((prev) => {
      const list = prev[chatID] ?? [];
      const nextList = list.includes(key) ? list.filter((x) => x !== key) : [...list, key];
      return { ...prev, [chatID]: nextList };
    });
    setDirtyKeys((prev) => new Set(prev).add(chatID));
  };

  const save = async (chatID: string) => {
    const next = { ...drafts };
    // 空数组 = 该群未配置，从落盘 scope 中移除
    setScope(next);
    try {
      await axios.put('/api/preferences/feishu-scope', { feishu_group_scope: next });
      setDirtyKeys((prev) => {
        const s = new Set(prev);
        s.delete(chatID);
        return s;
      });
    } catch {
      // ignore
    }
  };

  return (
    <section className="mb-8" data-section-id="feishu-scope" data-settings-tags="飞书 群 白名单 问答 feishu scope whitelist">
      <div className="flex items-center gap-2 mb-3">
        <Bot size={18} className="text-[#07c160]" />
        <h3 className="text-base font-bold text-[#1d1d1f] dk-text">飞书群问答白名单</h3>
      </div>
      <p className="text-sm text-gray-400 mb-4">
        为每个飞书群设置可访问的微信群/联系人。只做白名单（群与私聊一视同仁）；未配置的飞书群默认放行。
      </p>

      {chatKeys.length === 0 && (
        <div className="text-sm text-gray-400 bg-white rounded-2xl border border-gray-100 p-6 dk-card dk-border">
          尚未枚举到飞书群。请确认 bot 已加入群聊，且飞书网关已把群列表上报到后端。
        </div>
      )}

      {chatKeys.map((chatID) => {
        const selected = drafts[chatID] ?? [];
        return (
          <div key={chatID} className="bg-white rounded-2xl border border-gray-100 p-6 mb-4 dk-card dk-border">
            <div className="flex items-center gap-2 mb-2">
              <h4 className="font-bold text-[#1d1d1f] dk-text">{chatNames[chatID] || chatID}</h4>
              {dirtyKeys.has(chatID) && (
                <button
                  onClick={() => save(chatID)}
                  className="ml-auto text-xs font-semibold px-3 py-1.5 bg-[#07c160] text-white rounded-lg hover:bg-[#06ad56] transition-colors"
                >
                  保存
                </button>
              )}
            </div>

            {/* 已选白名单 */}
            <div className="min-h-[40px] flex flex-wrap gap-2 mb-3">
              {selected.length === 0 && <span className="text-sm text-gray-400 self-center">未配置，默认放行全部</span>}
              {selected.map((key) => (
                <span key={key} className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-full text-sm font-medium bg-[#07c160]/10 text-[#07c160]">
                  {labelForKey(key)}
                  <button onClick={() => toggleKey(chatID, key)} className="hover:text-red-500 transition-colors">
                    <X size={13} />
                  </button>
                </span>
              ))}
            </div>

            {/* 微信/私聊选择 */}
            <div className="text-xs text-gray-400 mb-2">点击下方条目加入白名单（再次点击移除）</div>
            <div className="mb-2">
              <div className="text-xs font-semibold text-gray-500 mb-1">微信群聊</div>
              <div className="flex flex-wrap gap-2 max-h-32 overflow-y-auto">
                {allGroups.map((g) => {
                  const key = 'group:' + g.username;
                  const on = selected.includes(key);
                  return (
                    <button
                      key={key}
                      onClick={() => toggleKey(chatID, key)}
                      className={`text-xs px-3 py-1.5 rounded-full border transition-colors ${
                        on ? 'bg-[#07c160] text-white border-[#07c160]' : 'bg-white border-gray-200 text-gray-600 hover:border-[#07c160] dk-card dk-border'
                      }`}
                    >
                      {g.name}
                    </button>
                  );
                })}
              </div>
            </div>
            <div>
              <div className="text-xs font-semibold text-gray-500 mb-1">私聊联系人</div>
              <div className="flex flex-wrap gap-2 max-h-32 overflow-y-auto">
                {allContacts.map((c) => {
                  const uname = c.username || '';
                  const key = 'contact:' + uname;
                  const on = selected.includes(key);
                  if (!uname) return null;
                  return (
                    <button
                      key={key}
                      onClick={() => toggleKey(chatID, key)}
                      className={`text-xs px-3 py-1.5 rounded-full border transition-colors ${
                        on ? 'bg-[#07c160] text-white border-[#07c160]' : 'bg-white border-gray-200 text-gray-600 hover:border-[#07c160] dk-card dk-border'
                      }`}
                    >
                      {c.remark || c.nickname || uname}
                    </button>
                  );
                })}
              </div>
            </div>
          </div>
        );
      })}
    </section>
  );
};
