/**
 * API 请求日志收集器
 *
 * 拦截全局 fetch，记录每次 API 调用的 URL、method、status、
 * 请求体摘要、响应体摘要。当响应不是 JSON（如返回 HTML 错误页）
 * 时，特别标记并保存原始响应文本，方便在日志页面调试。
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

function notify() {
  listeners.forEach(fn => fn());
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

function addEntry(entry: Omit<ApiLogEntry, 'id'>) {
  const full: ApiLogEntry = { ...entry, id: nextId++ };
  entries.unshift(full); // 最新的放最前面
  if (entries.length > MAX_ENTRIES) {
    entries = entries.slice(0, MAX_ENTRIES);
  }
  persist();
  notify();
}

/**
 * 初始化 fetch 拦截。在 main.tsx 中调用一次。
 *
 * 拦截策略：
 *   - 包装 window.fetch，记录请求/响应信息
 *   - 当响应 Content-Type 不是 application/json 时，读取响应文本并标记 nonJsonResponse
 *   - 当 fetch 本身抛错（网络断开等），记录 error 级别日志
 */
export function initApiLogger() {
  loadFromStorage();
  const originalFetch = window.fetch;
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = typeof input === 'string' ? input
      : input instanceof URL ? input.toString()
      : input.url;
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

      // 如果响应不是 JSON，克隆并读取文本（可能是 HTML 错误页）
      const isJson = contentType.includes('application/json');
      let responseSnippet = '';
      let nonJsonResponse = false;

      if (!isJson) {
        nonJsonResponse = true;
        try {
          const cloned = response.clone();
          const text = await cloned.text();
          responseSnippet = truncate(text, SNIPPET_LENGTH);
        } catch {
          responseSnippet = '[无法读取响应体]';
        }
      }

      // 对于非 JSON 响应（如 502 HTML 错误页），记录为 error
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
