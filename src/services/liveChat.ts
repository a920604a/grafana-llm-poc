import { isLiveChannelMessageEvent, isLiveChannelStatusEvent, LiveChannelConnectionState, LiveChannelEvent } from '@grafana/data';
import { getGrafanaLiveSrv } from '@grafana/runtime';
import { Observable } from 'rxjs';
import { ChatCommandResponse, ChatStreamEvent, isChatStreamEvent } from './contracts';

export type AssistantLiveEvent = { kind: 'status'; state: LiveChannelConnectionState; error?: unknown } | { kind: 'message'; event: ChatStreamEvent };
export function subscribeChat(command: ChatCommandResponse): Observable<AssistantLiveEvent> {
  return new Observable((subscriber) => {
    const subscription = getGrafanaLiveSrv().getStream<ChatStreamEvent>(command.channel).subscribe({
      next: (event: LiveChannelEvent<ChatStreamEvent>) => {
        if (isLiveChannelStatusEvent(event)) { subscriber.next({ kind: 'status', state: event.state, error: event.error }); }
        else if (isLiveChannelMessageEvent(event) && isChatStreamEvent(event.message)) { subscriber.next({ kind: 'message', event: event.message }); }
      },
      error: (error) => subscriber.error(error), complete: () => subscriber.complete(),
    });
    return () => subscription.unsubscribe();
  });
}
