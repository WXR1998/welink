/**
 * API 请求日志收集器
 *
 * 拦截全局 fetch，记录每次 API 调用的 URL、method、status、
 * 请求体摘要、响应体摘要。当响应不是 JSON（如返回 HTML 错误页）
 * 时，特别标记并保存原始响应文本，方便在日志页面调试。
 *
 * 对于 SSE 流式响应（text/event-stream），使用 TransformStream
 * 透传的同时累积响应体内容，实时更新日志条目。
 */

export type LogLevel = 'error' | 'warn' | 'info';

export interface ApiLogEntry {
  id: number;
  timestamp: string;
  level: LogLevel;
  method: string;
  url: string;
  status: number | null;
  statusText: string;
  durationMs: number;
  requestSnippet: string; // 请求体摘要（截断）
  responseSnippet: string; // 响应体摘要（截断）
  error: string; // 错误信息
  nonJsonResponse: boolean; // 响应不是 JSON（如 HTML 错误页）
}

const MAX_ENTRIES = 500;
const SNIPPET_LENGTH = 2000;
const STORAGE_KEY = 'welink:api-logs';
const FLUSH_DELAY = 1000; // 1 秒批量写 localStorage

let entries: ApiLogEntry[] = [];
let nextId = 1;
let listeners: Set<() => void> = new Set();
let flushTimer: ReturnType<typeof setTimeout> | null = null;
let notifyTimer: ReturnType<typeof setTimeout> | null = null;

// 轮询端点：这些请求不记入日志，避免刷屏
const SKIP_PATTERNS = [
  '/ai/llm-logs',        // 自身轮询
  '/token-stats',        // Token 统计轮询
  '/tasks/feed',         // 任务 feed 轮询
  '/ai/vec/all-jobs',    // 所有构建任务轮询
  '/ai/vec/build-progress', // 向量构建进度轮询
  '/ai/mem/status',      // 记忆提取状态轮询
  '/ai/conversations',   // 对话历史轮询
  '/ai/vec/index-status', // 向量索引状态轮询
  '/ai/rag',             // RAG 对话轮询
];

function shouldSkip(url: string): boolean {
  return SKIP_PATTERNS.some(p => url.includes(p));
}

function notify() {
  listeners.forEach(fn => fn());
}

function notifyDebounced() {
  if (notifyTimer) clearTimeout(notifyTimer);
  notifyTimer = setTimeout(() => {
    notify();
  }, 300);
}

export function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function getEntries(): ApiLogEntry[] {
  return entries;
}

export function clearEntries() {
  entries = [];
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch { /* ignore */ }
  notify();
}

function persist() {
  if (flushTimer) clearTimeout(flushTimer);
  flushTimer = setTimeout(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({
        entries: entries.slice(0, MAX_ENTRIES),
        nextId,
      }));
    } catch {
      // localStorage 满了或不可用，静默忽略
    }
  }, FLUSH_DELAY);
}

function loadFromStorage() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const data = JSON.parse(raw) as { entries: ApiLogEntry[]; nextId: number };
    entries = data.entries || [];
    nextId = data.nextId || 1;
  } catch {
    // 数据损坏，忽略
  }
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s;
  return s.slice(0, max) + '…[truncated]';
}

function addEntry(entry: Omit<ApiLogEntry, 'id'>): number {
  const full: ApiLogEntry = { ...entry, id: nextId++ };
  entries.unshift(full); // 最新的放最前面
  if (entries.length > MAX_ENTRIES) {
    entries = entries.slice(0, MAX_ENTRIES);
  }
  persist();
  notify();
  return full.id;
}

function updateEntry(id: number, partial: Partial<ApiLogEntry>) {
  const entry = entries.find(e => e.id === id);
  if (!entry) return;
  Object.assign(entry, partial);
  persist();
  notifyDebounced();
}

/**
 * 初始化 fetch 拦截。在 main.tsx 中调用一次。
 *
 * 拦截策略：
 *   - 包装 window.fetch，记录请求/响应信息
 *   - SSE 流式响应使用 TransformStream 透传 + 累积内容
 *   - 非 SSE 响应克隆后读取 body 供日志展示
 *   - 当 fetch 本身抛错（网络断开等），记录 error 级别日志
 */
export function initApiLogger() {
  loadFromStorage();
  const originalFetch = window.fetch;
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = typeof input === 'string' ? input
      : input instanceof URL ? input.toString()
      : input.url;

    // 跳过轮询端点，避免刷屏 + OOM
    if (shouldSkip(url)) {
      return originalFetch(input as RequestInfo, init);
    }

    const method = (init?.method || 'GET').toUpperCase();
    const startTime = performance.now();

    // 记录请求体摘要
    let requestSnippet = '';
    if (init?.body) {
      const bodyStr = typeof init.body === 'string' ? init.body : '';
      requestSnippet = truncate(bodyStr, SNIPPET_LENGTH);
    }

    try {
      const response = await originalFetch(input as RequestInfo, init);

      const durationMs = Math.round(performance.now() - startTime);
      const contentType = response.headers.get('content-type') || '';

      // SSE 流式响应：使用 TransformStream 透传 + 累积内容
      const isSSE = contentType.includes('text/event-stream');

      if (isSSE && response.body) {
        const entryId = addEntry({
          timestamp: new Date().toISOString(),
          level: 'info',
          method,
          url,
          status: response.status,
          statusText: response.statusText,
          durationMs,
          requestSnippet,
          responseSnippet: '[SSE 接收中…]',
          error: '',
          nonJsonResponse: false,
        });

        let accumulated = '';
        const decoder = new TextDecoder();

        const transformedBody = response.body.pipeThrough(new TransformStream({
          transform(chunk: Uint8Array, controller: TransformStreamDefaultController) {
            accumulated += decoder.decode(chunk, { stream: true });
            // 限制累积大小，保留最后部分
            if (accumulated.length > SNIPPET_LENGTH * 4) {
              accumulated = '…[earlier data truncated]\n' + accumulated.slice(-SNIPPET_LENGTH * 3);
            }
            controller.enqueue(chunk);
            updateEntry(entryId, { responseSnippet: truncate(accumulated, SNIPPET_LENGTH) });
          },
          flush() {
            updateEntry(entryId, { responseSnippet: truncate(accumulated, SNIPPET_LENGTH) });
          },
        }));

        return new Response(transformedBody, {
          headers: response.headers,
          status: response.status,
          statusText: response.statusText,
        });
      }

      // 非 SSE 响应：克隆后读取 body 供日志展示
      const isJson = contentType.includes('application/json');
      let responseSnippet = '';
      let nonJsonResponse = false;

      if (!isJson) {
        nonJsonResponse = true;
      }

      try {
        const cloned = response.clone();
        const text = await cloned.text();
        responseSnippet = truncate(text, SNIPPET_LENGTH);
      } catch {
        responseSnippet = '[无法读取响应体]';
      }

      const level: LogLevel = nonJsonResponse ? 'error' : (response.status >= 400 ? 'error' : 'info');

      addEntry({
        timestamp: new Date().toISOString(),
        level,
        method,
        url,
        status: response.status,
        statusText: response.statusText,
        durationMs,
        requestSnippet,
        responseSnippet,
        error: '',
        nonJsonResponse,
      });

      return response;
    } catch (err) {
      const durationMs = Math.round(performance.now() - startTime);
      const errorMsg = err instanceof Error ? err.message : String(err);

      addEntry({
        timestamp: new Date().toISOString(),
        level: 'error',
        method,
        url,
        status: null,
        statusText: '',
        durationMs,
        requestSnippet,
        responseSnippet: '',
        error: errorMsg,
        nonJsonResponse: false,
      });

      throw err;
    }
  };
}
