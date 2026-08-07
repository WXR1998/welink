/**
 * AI 设置 —— 独立主页 tab
 *
 * 从设置页抽出，避免与通用设置混合后占太多篇幅：
 *  - AI 配置（分析模型 / Embedding / 记忆 / 生图 / Rerank）
 *  - Prompt 模板
 *  - 朗读 TTS
 */

import React from 'react';
import { Sparkles } from 'lucide-react';
import { Header } from '../../layout/Header';
import { AIConfigGroup } from './AIConfigGroup';
import { PromptTemplateSection } from './PromptTemplateSection';
import { TtsSection } from './TtsSection';

export const AISettingsPage: React.FC = () => {
  return (
    <div>
      <Header
        title="AI 设置"
        subtitle="配置对话分析、语义搜索、记忆提炼、AI 生图与播客朗读所用的模型服务"
      />
      <div className="max-w-2xl">
        <AIConfigGroup />
        <PromptTemplateSection />
        <TtsSection />
      </div>
      <div className="mt-8 pt-6 border-t border-gray-100 dark:border-white/10 flex items-center gap-2 text-xs text-gray-400">
        <Sparkles size={14} />
        其他通用设置仍在设置页中，如隐私屏蔽、屏幕锁定、多账号与系统配置。
      </div>
    </div>
  );
};

export default AISettingsPage;
