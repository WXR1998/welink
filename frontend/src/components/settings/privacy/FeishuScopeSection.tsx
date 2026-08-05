import React, { useEffect, useMemo, useState } from 'react';
import axios from 'axios';
import { Bot, Search, X } from 'lucide-react';
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
  // group_username → [member_wxid, ...]，用于按群友名筛选群
  const [memberships, setMemberships] = useState<Record<string, string[]>>({});

  useEffect(() => {
    axios
      .get<ScopeData>('/api/preferences/feishu-scope')
      .then((r) => {
        setScope(r.data.feishu_group_scope ?? {});
        setDrafts(r.data.feishu_group_scope ?? {});
        setChatNames(r.data.feishu_bot_chats ?? {});
      })
      .catch(() => {});
    axios
      .get<Record<string, string[]>>('/api/contacts/room-memberships')
      .then((r) => setMemberships(r.data ?? {}))
      .catch(() => {});
  }, []);

  // 合并：上报的飞书群(chatNames) + 已配置白名单的 chat_id（去重保序）
  const chatKeys = Array.from(new Set([...Object.keys(chatNames), ...Object.keys(scope), ...Object.keys(drafts)]));

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

  // member wxid → display name，用于群友名搜索
  const memberNameMap = useMemo(() => {
    const m = new Map<string, string>();
    for (const c of allContacts) {
      if (c.username) m.set(c.username, c.remark || c.nickname || c.username);
    }
    return m;
  }, [allContacts]);

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

      {chatKeys.map((chatID) => (
        <FeishuGroupCard
          key={chatID}
          chatID={chatID}
          chatName={chatNames[chatID] || chatID}
          selected={drafts[chatID] ?? []}
          dirty={dirtyKeys.has(chatID)}
          allGroups={allGroups}
          allContacts={allContacts}
          memberships={memberships}
          memberNameMap={memberNameMap}
          groupLabel={groupLabel}
          contactLabel={contactLabel}
          labelForKey={labelForKey}
          toggleKey={toggleKey}
          save={save}
        />
      ))}
    </section>
  );
};

interface FeishuGroupCardProps {
  chatID: string;
  chatName: string;
  selected: string[];
  dirty: boolean;
  allGroups: GroupInfo[];
  allContacts: ContactStats[];
  memberships: Record<string, string[]>;
  memberNameMap: Map<string, string>;
  groupLabel: (id: string) => string;
  contactLabel: (id: string) => string;
  labelForKey: (key: string) => string;
  toggleKey: (chatID: string, key: string) => void;
  save: (chatID: string) => void;
}

const FeishuGroupCard: React.FC<FeishuGroupCardProps> = ({
  chatID,
  chatName,
  selected,
  dirty,
  allGroups,
  allContacts,
  memberships,
  memberNameMap,
  groupLabel,
  contactLabel,
  labelForKey,
  toggleKey,
  save,
}) => {
  const [groupQuery, setGroupQuery] = useState('');
  const [contactQuery, setContactQuery] = useState('');

  // 筛选群聊：按群名或群友名匹配
  const filteredGroups = useMemo(() => {
    const q = groupQuery.trim().toLowerCase();
    if (!q) return allGroups;
    return allGroups.filter((g) => {
      if (g.name.toLowerCase().includes(q)) return true;
      // 检查群友名
      const memberWxids = memberships[g.username] ?? [];
      for (const wxid of memberWxids) {
        const name = memberNameMap.get(wxid);
        if (name && name.toLowerCase().includes(q)) return true;
      }
      return false;
    });
  }, [allGroups, groupQuery, memberships, memberNameMap]);

  const filteredContacts = useMemo(() => {
    const q = contactQuery.trim().toLowerCase();
    if (!q) return allContacts;
    return allContacts.filter((c) => {
      const name = (c.remark || c.nickname || c.username || '').toLowerCase();
      return name.includes(q);
    });
  }, [allContacts, contactQuery]);

  return (
    <div className="bg-white rounded-2xl border border-gray-100 p-6 mb-4 dk-card dk-border">
      <div className="flex items-center gap-2 mb-2">
        <h4 className="font-bold text-[#1d1d1f] dk-text">{chatName}</h4>
        {dirty && (
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

      {/* 微信群聊 */}
      <div className="mb-2">
        <div className="text-xs font-semibold text-gray-500 mb-1">微信群聊</div>
        <div className="relative mb-2">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            value={groupQuery}
            onChange={(e) => setGroupQuery(e.target.value)}
            placeholder="搜索群名或群友名…"
            className="w-full text-sm pl-9 pr-3 py-2 rounded-lg border border-gray-200 focus:border-[#07c160] focus:outline-none dk-card dk-border"
          />
        </div>
        <div className="flex flex-wrap gap-2 max-h-32 overflow-y-auto">
          {filteredGroups.map((g) => {
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
          {filteredGroups.length === 0 && (
            <span className="text-xs text-gray-400">无匹配群聊</span>
          )}
        </div>
      </div>

      {/* 私聊联系人 */}
      <div>
        <div className="text-xs font-semibold text-gray-500 mb-1">私聊联系人</div>
        <div className="relative mb-2">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            value={contactQuery}
            onChange={(e) => setContactQuery(e.target.value)}
            placeholder="搜索联系人…"
            className="w-full text-sm pl-9 pr-3 py-2 rounded-lg border border-gray-200 focus:border-[#07c160] focus:outline-none dk-card dk-border"
          />
        </div>
        <div className="flex flex-wrap gap-2 max-h-32 overflow-y-auto">
          {filteredContacts.map((c) => {
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
          {filteredContacts.length === 0 && (
            <span className="text-xs text-gray-400">无匹配联系人</span>
          )}
        </div>
      </div>
    </div>
  );
};
