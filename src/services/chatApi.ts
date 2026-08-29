import { getBackendSrv } from '@grafana/runtime';
import { lastValueFrom } from 'rxjs';
import pluginJson from '../plugin.json';
import { ChatCommandResponse, ChatMessageDto } from './contracts';

const resourceBase = `/api/plugins/${pluginJson.id}/resources/v1`;
export async function createChat(messages: ChatMessageDto[], sessionId?: string): Promise<ChatCommandResponse> {
  const response = await lastValueFrom(getBackendSrv().fetch<ChatCommandResponse>({
    method: 'POST', url: `${resourceBase}/chat`, data: { clientRequestId: crypto.randomUUID(), sessionId, messages }, showErrorAlert: false,
  }));
  return response.data;
}
export async function cancelChat(requestId: string): Promise<void> {
  await lastValueFrom(getBackendSrv().fetch({ method: 'DELETE', url: `${resourceBase}/chat/${encodeURIComponent(requestId)}`, showErrorAlert: false }));
}
