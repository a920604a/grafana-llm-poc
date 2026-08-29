import { ChatStreamEvent } from '../services/contracts';
export interface ConversationMessage { id: string; role: 'user' | 'assistant'; content: string; status: 'complete' | 'pending' | 'streaming' | 'error' | 'cancelled'; error?: string; }
export interface ChatState { messages: ConversationMessage[]; requestId?: string; sessionId?: string; model: string; expectedSequence: number; connection: 'idle' | 'connecting' | 'connected' | 'disconnected' | 'error'; }
export const initialChatState: ChatState = { messages: [], model: '尚未連線', expectedSequence: 0, connection: 'idle' };
export type ChatAction =
  | { type: 'send'; user: ConversationMessage; assistant: ConversationMessage }
  | { type: 'accepted'; requestId: string; sessionId: string; model: string }
  | { type: 'connection'; connection: ChatState['connection'] }
  | { type: 'event'; event: ChatStreamEvent }
  | { type: 'failed'; message: string }
  | { type: 'cancelled' };
function updateAssistant(state: ChatState, update: (message: ConversationMessage) => ConversationMessage): ChatState {
  const messages = [...state.messages];
  for (let index = messages.length - 1; index >= 0; index--) { if (messages[index].role === 'assistant') { messages[index] = update(messages[index]); break; } }
  return { ...state, messages };
}
export function chatReducer(state: ChatState, action: ChatAction): ChatState {
  switch (action.type) {
    case 'send': return { ...state, messages: [...state.messages, action.user, action.assistant], expectedSequence: 0, connection: 'connecting' };
    case 'accepted': return { ...state, requestId: action.requestId, sessionId: action.sessionId, model: action.model };
    case 'connection': return { ...state, connection: action.connection };
    case 'event': {
      if (action.event.sequence < state.expectedSequence) { return state; }
      if (action.event.sequence !== state.expectedSequence) { return updateAssistant({ ...state, connection: 'error' }, (m) => ({ ...m, status: 'error', error: '串流事件不連續，請重試。' })); }
      const next = { ...state, expectedSequence: state.expectedSequence + 1 };
      if (action.event.type === 'start') { return updateAssistant({ ...next, connection: 'connected', model: action.event.model }, (m) => ({ ...m, status: 'streaming' })); }
      if (action.event.type === 'token') { const content = action.event.content; return updateAssistant(next, (m) => ({ ...m, status: 'streaming', content: m.content + content })); }
	  if (action.event.type === 'draft') { return next; }
      if (action.event.type === 'complete') { return updateAssistant({ ...next, requestId: undefined }, (m) => ({ ...m, status: 'complete' })); }
      const message = action.event.error.message;
      return updateAssistant({ ...next, requestId: undefined, connection: 'error' }, (m) => ({ ...m, status: 'error', error: message }));
    }
    case 'failed': return updateAssistant({ ...state, requestId: undefined, connection: 'error' }, (m) => ({ ...m, status: 'error', error: action.message }));
    case 'cancelled': return updateAssistant({ ...state, requestId: undefined, connection: 'idle' }, (m) => ({ ...m, status: 'cancelled', error: '已取消' }));
  }
}
