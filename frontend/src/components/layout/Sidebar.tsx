/**
 * 侧边栏组件 - 桌面侧边（可折叠）/ 手机底部导航栏
 */

import { useState } from 'react';
import { Bot, BarChart2, Database, Sun, Moon, MessagesSquare, MessageCircle, BookOpen, Github, Search, Hourglass, Link2, X, Settings, ChevronLeft, ChevronRight, Sparkles, Download, Brain, FlaskConical, CalendarClock, ScrollText, Grid } from 'lucide-react';
import type { TabType } from '../../types';

interface SidebarProps {
  activeTab: TabType;
  onTabChange: (tab: TabType) => void;
  dark: boolean;
  onToggleDark: () => void;
  badges?: Partial<Record<TabType, number>>;
}

// 导航项右上角的未读角标（折叠/展开/手机端通用）
const NavBadge: React.FC<{ count?: number }> = ({ count }) => {
  if (!count || count <= 0) return null;
  return (
    <span className="absolute -top-1.5 -right-1.5 min-w-[14px] h-[14px] px-1 rounded-full bg-red-500 text-white text-[9px] font-bold flex items-center justify-center leading-none shadow-sm">
      {count > 99 ? '99+' : count}
    </span>
  );
};

// WKWebView UA 含 AppleWebKit 但不含 Safari / Chrome / Firefox
const isWebView = () => {
  const ua = navigator.userAgent;
  return ua.includes('AppleWebKit') && !ua.includes('Safari') && !ua.includes('Chrome') && !ua.includes('Firefox');
};

const openExternal = (url: string) => {
  if (isWebView()) {
    fetch(`/api/open-url?url=${encodeURIComponent(url)}`);
  } else {
    window.open(url, '_blank');
  }
};

// 手机底部导航：主 tab + “更多”按钮
const MOBILE_PRIMARY: { tab: TabType; icon: React.ReactNode; label: string }[] = [
  { tab: 'dashboard', icon: <Bot size={20} strokeWidth={2} />,             label: 'AI 首页' },
  { tab: 'groups',    icon: <MessagesSquare size={20} strokeWidth={2} />, label: '群聊' },
  { tab: 'search',    icon: <Search size={20} strokeWidth={2} />,         label: '搜索' },
  { tab: 'memory',    icon: <Brain size={20} strokeWidth={2} />,           label: '记忆库' },
  { tab: 'apilogs',   icon: <ScrollText size={20} strokeWidth={2} />,      label: '日志' },
];

export const Sidebar: React.FC<SidebarProps> = ({ activeTab, onTabChange, dark, onToggleDark, badges }) => {
  const [swaggerOpen, setSwaggerOpen] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);
  const [expanded, setExpanded] = useState(() => {
    return localStorage.getItem('welink_sidebar_expanded') !== 'false';
  });

  const toggleExpanded = () => {
    const next = !expanded;
    setExpanded(next);
    localStorage.setItem('welink_sidebar_expanded', String(next));
  };

  const navItems: { tab: TabType; icon: React.ReactNode; label: string }[] = [
    { tab: 'dashboard', icon: <Bot size={20} strokeWidth={2} />,              label: 'AI 首页' },
    { tab: 'stats',     icon: <BarChart2 size={20} strokeWidth={2} />,        label: '洞察' },
    { tab: 'contacts',  icon: <MessageCircle size={20} strokeWidth={2} />,    label: '私聊' },
    { tab: 'groups',    icon: <MessagesSquare size={20} strokeWidth={2} />,   label: '群聊' },
    { tab: 'calendar',    icon: <Hourglass size={20} strokeWidth={2} />,        label: '时光机' },
    { tab: 'search',      icon: <Search size={20} strokeWidth={2} />,         label: '搜索' },
    { tab: 'urls',        icon: <Link2 size={20} strokeWidth={2} />,           label: '链接' },
    { tab: 'skills',      icon: <Sparkles size={20} strokeWidth={2} />,        label: 'Skills' },
    { tab: 'labs',        icon: <FlaskConical size={20} strokeWidth={2} />,    label: '创意实验室' },
    { tab: 'memory',      icon: <Brain size={20} strokeWidth={2} />,           label: '记忆库' },
    { tab: 'tasks',       icon: <CalendarClock size={20} strokeWidth={2} />,   label: '定时任务' },
    { tab: 'export',      icon: <Download size={20} strokeWidth={2} />,        label: '导出中心' },
    { tab: 'db',        icon: <Database size={20} strokeWidth={2} />,         label: '数据库' },
    { tab: 'apilogs',   icon: <ScrollText size={20} strokeWidth={2} />,        label: 'API日志' },
    { tab: 'settings',  icon: <Settings size={20} strokeWidth={2} />,         label: '设置' },
  ];

  const bottomItems = [
    { icon: <BookOpen size={18} strokeWidth={2} />,  label: 'API 文档', onClick: () => setSwaggerOpen(true) },
    { icon: <Github size={18} strokeWidth={2} />,    label: 'GitHub',   onClick: () => openExternal('https://github.com/runzhliu/WeLink') },
    { icon: dark ? <Sun size={18} strokeWidth={2} /> : <Moon size={18} strokeWidth={2} />, label: dark ? '亮色模式' : '暗色模式', onClick: onToggleDark },
  ];

  return (
    <>
      {swaggerOpen && (
        <div className="fixed inset-0 z-50 flex flex-col bg-white dark:bg-[#1d1d1f]">
          <div className="flex items-center justify-between px-4 py-2 border-b dk-border">
            <span className="text-sm font-semibold text-gray-700 dark:text-gray-200">API 文档</span>
            <button
              onClick={() => setSwaggerOpen(false)}
              className="p-2 rounded-xl text-gray-400 hover:text-gray-700 hover:bg-gray-100 dark:hover:bg-white/10 transition-all"
            >
              <X size={18} />
            </button>
          </div>
          <iframe src="/swagger/" className="flex-1 w-full border-0" />
        </div>
      )}

      {/* 桌面侧边栏 */}
      <aside
        data-tour="sidebar"
        className={`hidden sm:flex dk-card bg-white dk-border border-r flex-col py-4 shadow-sm z-10 transition-all duration-200 ${
          expanded ? 'w-44' : 'w-16'
        } overflow-hidden`}
      >
        {/* Logo 区（固定） */}
        <div className={`shrink-0 flex items-center gap-3 px-3 mb-4 ${expanded ? 'justify-between' : 'justify-center flex-col gap-2'}`}>
          <div
            className="w-9 h-9 rounded-xl overflow-hidden shadow-md shadow-green-100/50 dark:shadow-none cursor-pointer hover:opacity-80 transition-opacity flex-shrink-0"
            onClick={() => openExternal('https://welink.click')}
            title="官方文档"
          >
            <img loading="lazy" src="/favicon.svg" alt="WeLink" className="w-full h-full" />
          </div>
          {expanded && (
            <span className="text-sm font-black text-[#1d1d1f] dark:text-white tracking-tight flex-1 truncate">WeLink</span>
          )}
          <button
            onClick={toggleExpanded}
            className="flex-shrink-0 p-1 rounded-lg text-gray-400 hover:text-gray-600 dark:hover:text-gray-200 hover:bg-gray-100 dark:hover:bg-white/10 transition-all"
            title={expanded ? '收起侧边栏' : '展开侧边栏'}
          >
            {expanded ? <ChevronLeft size={15} /> : <ChevronRight size={15} />}
          </button>
        </div>

        {/* 主导航（中间可滚动） */}
        <nav className="flex flex-col gap-1 flex-1 min-h-0 overflow-y-auto px-2">
          {navItems.map(({ tab, icon, label }) => (
            <button
              key={tab}
              data-tour={`nav-${tab}`}
              onClick={() => onTabChange(tab)}
              title={expanded ? undefined : label}
              className={`flex items-center gap-3 rounded-xl transition-all duration-150 ${
                expanded ? 'px-3 py-2.5' : 'p-3 justify-center'
              } ${
                activeTab === tab
                  ? 'bg-[#e7f8f0] text-[#07c160] dark:bg-[#07c160]/20'
                  : 'text-gray-400 hover:text-gray-600 hover:bg-gray-50 dark:hover:bg-white/5'
              }`}
            >
              <span className="flex-shrink-0 relative">
                {icon}
                <NavBadge count={badges?.[tab]} />
              </span>
              {expanded && (
                <span className="text-xs font-semibold truncate">{label}</span>
              )}
            </button>
          ))}
        </nav>

        {/* 底部工具按钮（固定） */}
        <div className="shrink-0 flex flex-col gap-1 px-2 pt-2 border-t border-gray-100 dark:border-white/10 mt-2">
          {bottomItems.map(({ icon, label, onClick }) => (
            <button
              key={label}
              onClick={onClick}
              title={expanded ? undefined : label}
              className={`flex items-center gap-3 rounded-xl text-gray-400 hover:text-gray-600 hover:bg-gray-50 dark:hover:bg-white/5 transition-all duration-150 ${
                expanded ? 'px-3 py-2' : 'p-2.5 justify-center'
              }`}
            >
              <span className="flex-shrink-0">{icon}</span>
              {expanded && (
                <span className="text-xs font-semibold truncate">{label}</span>
              )}
            </button>
          ))}
        </div>
      </aside>

      {/* 手机底部导航栏 —— 4 个主要 tab + 更多按钮 */}
      <nav
        data-tour="sidebar"
        className="sm:hidden fixed bottom-0 left-0 right-0 z-50 bg-white border-t border-gray-200 dark:bg-[#1d1d1f] dark:border-white/10 flex w-full safe-area-bottom"
      >
        {MOBILE_PRIMARY.map(({ tab, icon, label }) => (
          <button
            key={tab}
            data-tour={`nav-${tab}`}
            onClick={() => onTabChange(tab)}
            className={`flex-1 flex flex-col items-center justify-center py-2 gap-0.5 transition-colors min-w-0 ${
              activeTab === tab ? 'text-[#07c160]' : 'text-gray-400'
            }`}
          >
            <span className="flex-shrink-0 relative">
              {icon}
              <NavBadge count={badges?.[tab]} />
            </span>
            <span className="text-[10px] font-semibold truncate w-full text-center px-0.5">{label}</span>
          </button>
        ))}

        {/* 更多按钮 —— 点击弹出剩余 tab 面板 */}
        <button
          onClick={() => setMoreOpen(v => !v)}
          className={`flex-1 flex flex-col items-center justify-center py-2 gap-0.5 transition-colors min-w-0 ${
            moreOpen ? 'text-[#07c160]' : 'text-gray-400'
          }`}
        >
          <Grid size={20} strokeWidth={2} />
          <span className="text-[10px] font-semibold">更多</span>
        </button>
      </nav>

      {/* 更多面板 —— 半屏高度，网格展示剩余 tab */}
      {moreOpen && (
        <div className="sm:hidden fixed inset-0 z-[60] flex flex-col justify-end" onClick={() => setMoreOpen(false)}>
          <div className="absolute inset-0 bg-black/30 backdrop-blur-sm" />
          <div
            className="relative dk-card bg-white dk-border border-t rounded-t-2xl safe-area-bottom max-h-[60vh] overflow-y-auto"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex justify-center pt-2.5 pb-1">
              <div className="w-10 h-1 rounded-full bg-gray-300 dark:bg-gray-600" />
            </div>
            <div className="px-3 pb-3">
              <div className="flex items-center justify-between mb-3 px-1">
                <span className="text-sm font-bold text-gray-700 dark:text-gray-200">全部功能</span>
                <button
                  onClick={() => setMoreOpen(false)}
                  className="p-1.5 rounded-lg text-gray-400 hover:text-gray-600 dark:hover:text-gray-200 hover:bg-gray-100 dark:hover:bg-white/10"
                >
                  <X size={18} />
                </button>
              </div>
              <div className="grid grid-cols-4 gap-1">
                {navItems.map(({ tab, icon, label }) => {
                  const isPrimary = MOBILE_PRIMARY.some(p => p.tab === tab);
                  if (isPrimary) return null;
                  return (
                    <button
                      key={tab}
                      onClick={() => { onTabChange(tab); setMoreOpen(false); }}
                      className={`flex flex-col items-center justify-center gap-1.5 py-3 px-1 rounded-xl transition-colors ${
                        activeTab === tab
                          ? 'bg-[#e7f8f0] text-[#07c160] dark:bg-[#07c160]/20'
                          : 'text-gray-500 hover:bg-gray-50 dark:hover:bg-white/5'
                      }`}
                    >
                      <span className="flex-shrink-0 relative">
                        {icon}
                        <NavBadge count={badges?.[tab]} />
                      </span>
                      <span className="text-[10px] font-semibold text-center leading-tight">{label}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          </div>
        </div>
      )}
    </>
  );
};
