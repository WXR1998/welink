import type { AIProfileTestResult } from './types';

export async function streamProfileTestResults(
  url: string,
  body: unknown,
  onResult: (result: AIProfileTestResult) => void,
): Promise<void> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(payload.error || '连接失败');
  }
  if (!response.body) throw new Error('浏览器不支持流式测试结果');

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  const consumeEvent = (rawEvent: string) => {
    const event = rawEvent.split('\n').find(line => line.startsWith('event:'))?.slice(6).trim();
    const data = rawEvent.split('\n').find(line => line.startsWith('data:'))?.slice(5).trim();
    if (event !== 'result' || !data) return;
    const payload = JSON.parse(data) as { result?: AIProfileTestResult };
    if (payload.result) onResult(payload.result);
  };

  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value, { stream: !done });
    let boundary = buffer.indexOf('\n\n');
    while (boundary >= 0) {
      consumeEvent(buffer.slice(0, boundary));
      buffer = buffer.slice(boundary + 2);
      boundary = buffer.indexOf('\n\n');
    }
    if (done) break;
  }
  if (buffer.trim()) consumeEvent(buffer);
}
