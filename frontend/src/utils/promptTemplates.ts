/**
 * AI Prompt 模板管理。模板内容由后端 AI SQLite 数据库提供。
 */

export interface PromptTemplate {
  id: string;
  name: string;
  description: string;
  prompt: string;
  default_prompt: string;
}

export function getPrompt(
  id: string,
  templates: PromptTemplate[],
  vars?: Record<string, string>,
): string {
  let prompt = templates.find(template => template.id === id)?.prompt ?? '';
  if (vars) {
    for (const [key, value] of Object.entries(vars)) {
      prompt = prompt.replaceAll(`{{${key}}}`, value);
    }
  }
  return prompt;
}

export async function loadPromptTemplates(): Promise<PromptTemplate[]> {
  try {
    const response = await fetch('/api/preferences/prompts');
    if (!response.ok) return [];
    const data = await response.json();
    return data?.templates ?? [];
  } catch {
    return [];
  }
}
