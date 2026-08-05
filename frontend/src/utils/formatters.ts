/**
 * 格式化工具函数
 */

/**
 * 格式化数字为千分位
 */
/**
 * 修复 react-markdown + remark-gfm 的一个渲染坑：块引用行写成
 * `> [说话人]: 内容` 时，英文冒号会被 GFM 误判为自动链接定义，
 * 导致整行被吞掉、只留下空 <blockquote>。
 * 这里只把块引用行开头的 `]: ` 改成 `]：`，保住引用内容与本来的方括号。
 */
export const preserveMarkdownBlockquote = (markdown: string): string => {
  if (!markdown) return markdown;
  return markdown.split('\n').map(line => {
    if (/^\s*>\s*\[[^\]]+\]:/.test(line)) {
      return line.replace(/^(\s*>\s*\[[^\]]+\]):/, '$1：');
    }
    return line;
  }).join('\n');
};


export const formatNumber = (num: number): string => {
  return num.toLocaleString('zh-CN');
};

/**
 * 格式化大数字（K, M, B）
 */
export const formatCompactNumber = (num: number): string => {
  if (num >= 1_000_000_000) {
    return (num / 1_000_000_000).toFixed(1) + 'B';
  }
  if (num >= 1_000_000) {
    return (num / 1_000_000).toFixed(1) + 'M';
  }
  if (num >= 1_000) {
    return (num / 1_000).toFixed(1) + 'K';
  }
  return num.toString();
};

/**
 * 格式化日期
 */
export const formatDate = (dateString: string): string => {
  if (!dateString || dateString === '-') return '-';
  try {
    const date = new Date(dateString);
    return date.toLocaleDateString('zh-CN', {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
    });
  } catch {
    return dateString;
  }
};

/**
 * 计算天数差
 */
export const daysSince = (dateString: string): number => {
  if (!dateString) return -1;
  try {
    const date = new Date(dateString);
    const now = new Date();
    const diff = now.getTime() - date.getTime();
    return Math.floor(diff / (1000 * 60 * 60 * 24));
  } catch {
    return -1;
  }
};

/**
 * 获取联系人显示名称
 */
export const getContactDisplayName = (contact: {
  remark?: string;
  nickname?: string;
  username?: string;
}): string => {
  return contact.remark || contact.nickname || contact.username || '未知';
};

/**
 * 获取群聊显示名称：当群名与原始群名不同时，显示为 `群名（群原名）`。
 */
export const getGroupDisplayName = (group: {
  name?: string;
  nickname?: string;
  username?: string;
}): string => {
  const display = group.name || group.username || '未知群聊';
  const original = group.nickname?.trim();
  if (original && original !== display) {
    return `${display}（${original}）`;
  }
  return display;
};

/**
 * 截断单条聊天记录内容，防止超长消息撑爆 prompt。
 * 按字符数（rune）计算，中英文均算 1 个字符。
 */
export const truncateMsgContent = (content: string, maxChars = 200): string => {
  if (!content) return '';
  const chars = Array.from(content);
  if (chars.length <= maxChars) return content;
  return chars.slice(0, maxChars).join('') + '…';
};
