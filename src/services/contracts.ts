import { LiveChannelScope } from '@grafana/data';

export type MessageRole = 'system' | 'user' | 'assistant';
export interface ChatMessageDto { role: MessageRole; content: string; }
export interface ChatCommandResponse {
  requestId: string; sessionId: string; model: string;
  channel: { scope: LiveChannelScope; namespace: string; path: string };
  expiresAt: string;
}
interface BaseEvent { version: 1; requestId: string; sessionId: string; sequence: number; timestamp: string; }
export type ChatStreamEvent =
  | (BaseEvent & { type: 'start'; model: string })
  | (BaseEvent & { type: 'token'; content: string })
  | (BaseEvent & { type: 'complete'; finishReason?: string })
  | (BaseEvent & { type: 'draft'; draftId: string; revision: number; title: string; warningCount: number })
  | (BaseEvent & { type: 'error'; error: { code: string; message: string; retryable: boolean } });
export function isChatStreamEvent(value: unknown): value is ChatStreamEvent {
  if (!value || typeof value !== 'object') { return false; }
  const event = value as Partial<ChatStreamEvent>;
  return event.version === 1 && typeof event.requestId === 'string' && typeof event.sessionId === 'string' &&
    typeof event.sequence === 'number' && (event.type === 'start' || event.type === 'token' || event.type === 'complete' || event.type === 'draft' || event.type === 'error');
}
