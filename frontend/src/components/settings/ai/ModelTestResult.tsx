import React from 'react';
import { AlertCircle, Check } from 'lucide-react';
import type { AIProfileTestResult } from './types';

const protocolLabels: Record<string, string> = {
  chat_completions: 'Chat Completions',
  responses: 'Responses',
  embedding: 'Embedding',
  rerank: 'Rerank',
  claude: 'Claude API',
  bedrock: 'Bedrock API',
  vertex: 'Vertex API',
};

const protocolLabel = (protocol: string) => protocolLabels[protocol] ?? protocol;

export const ModelTestResult: React.FC<{ result: AIProfileTestResult }> = ({ result }) => (
  <div className={`rounded-lg border px-3 py-2 text-xs ${result.ok ? 'border-green-100 bg-green-50/60 dark:border-green-500/20 dark:bg-green-500/10' : 'border-red-100 bg-red-50/60 dark:border-red-500/20 dark:bg-red-500/10'}`}>
    <div className={`flex items-center gap-1.5 font-semibold ${result.ok ? 'text-[#07a958]' : 'text-red-500'}`}>
      {result.ok ? <Check size={13} /> : <AlertCircle size={13} />}
      <span>{result.name}：{result.ok ? '测试完成' : '测试失败'}</span>
      <span className="font-normal text-gray-500 dark:text-gray-400">{result.provider} · {result.model}</span>
    </div>
    <div className="mt-1.5 space-y-1 text-gray-600 dark:text-gray-300">
      {result.protocols.map(protocol => (
        <div key={protocol.protocol} className={protocol.ok ? '' : 'text-red-500'}>
          {protocolLabel(protocol.protocol)}：{protocol.ok ? '成功' : protocol.error || '失败'}
          {protocol.latency_ms > 0 && ` · ${protocol.latency_ms}ms`}
          {protocol.tokens_per_second && protocol.tokens_per_second > 0 && ` · ${protocol.tokens_per_second.toFixed(1)} tok/s`}
        </div>
      ))}
    </div>
    {!result.protocols.length && result.error && <div className="mt-1.5 text-red-500">{result.error}</div>}
    {result.ok && result.selected_protocol_ok === false && result.selected_protocol && (
      <div className="mt-1.5 text-amber-600 dark:text-amber-400">
        当前启用的 {protocolLabel(result.selected_protocol)} 不可用，请切换到通过的协议。
      </div>
    )}
  </div>
);
