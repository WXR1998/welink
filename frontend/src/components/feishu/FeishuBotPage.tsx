/**
 * 飞书机器人 — 独立主页
 *
 * 汇集与飞书机器人问答相关的管理控件：
 *  - 飞书群问答白名单（含"补充给 AI 的信息"）
 *  - 飞书机器人会话上下文管理（查看 / 删除）
 */
import React from 'react';
import { Bot } from 'lucide-react';
import { Header } from '../layout/Header';
import { FeishuScopeSection } from '../settings/privacy/FeishuScopeSection';
import { FeishuContextSection } from '../settings/privacy/FeishuContextSection';
import type { ContactStats, GroupInfo } from '../../types';

interface FeishuBotPageProps {
  allContacts: ContactStats[];
  allGroups: GroupInfo[];
}

export const FeishuBotPage: React.FC<FeishuBotPageProps> = ({ allContacts, allGroups }) => {
  return (
    <div>
      <Header
        title="飞书机器人"
        subtitle="管理飞书群的问答白名单、补充信息与会话上下文"
      />
      <div className="max-w-3xl">
        <FeishuScopeSection allContacts={allContacts} allGroups={allGroups} />
        <FeishuContextSection />
      </div>
      <div className="mt-8 pt-6 border-t border-gray-100 dark:border-white/10 flex items-center gap-2 text-xs text-gray-400">
        <Bot size={14} />
        飞书机器人问答走跨联系人 AI 检索，各群/各人的上下文互相独立。
      </div>
    </div>
  );
};

export default FeishuBotPage;
