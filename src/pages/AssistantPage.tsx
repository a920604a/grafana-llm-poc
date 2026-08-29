import React, { FormEvent, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2, LiveChannelConnectionState } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import { Alert, Badge, Button, Field, Input, TextArea, useStyles2 } from '@grafana/ui';
import { Subscription } from 'rxjs';
import { cancelChat, createChat } from '../services/chatApi';
import { ChatMessageDto } from '../services/contracts';
import { applyDashboardDraft, createDashboardDraft, DashboardDraft, DashboardOperation, getDashboardDraft } from '../services/dashboardApi';
import { subscribeChat } from '../services/liveChat';
import { chatReducer, initialChatState } from '../state/chatReducer';

const connectionMap: Record<string, { text: string; color: 'green' | 'blue' | 'orange' | 'red' }> = {
  idle: { text: '待命', color: 'blue' }, connecting: { text: '連線中', color: 'blue' }, connected: { text: '串流中', color: 'green' },
  disconnected: { text: '已中斷', color: 'orange' }, error: { text: '錯誤', color: 'red' },
};

export default function AssistantPage() {
  const styles = useStyles2(getStyles);
  const [state, dispatch] = useReducer(chatReducer, initialChatState);
  const [mode, setMode] = useState<'chat' | 'dashboard'>('chat');
  const [operation, setOperation] = useState<DashboardOperation>('create_dashboard');
  const [input, setInput] = useState('');
  const [folderUid, setFolderUid] = useState('');
  const [datasourceUids, setDatasourceUids] = useState('');
  const [dashboardUid, setDashboardUid] = useState('');
  const [draft, setDraft] = useState<DashboardDraft>();
  const [applying, setApplying] = useState(false);
  const subscriptionRef = useRef<Subscription | undefined>(undefined);
  const active = Boolean(state.requestId) || state.connection === 'connecting';
  const badge = connectionMap[state.connection];
  useEffect(() => () => subscriptionRef.current?.unsubscribe(), []);
  const history = useMemo<ChatMessageDto[]>(() => state.messages.filter((m) => m.status === 'complete').map(({ role, content }) => ({ role, content })), [state.messages]);

  async function send(event: FormEvent) {
    event.preventDefault();
    const content = input.trim();
    if (!content || active) { return; }
    setDraft(undefined);
    dispatch({ type: 'send', user: { id: crypto.randomUUID(), role: 'user', content, status: 'complete' }, assistant: { id: crypto.randomUUID(), role: 'assistant', content: '', status: 'pending' } });
    try {
      const command = mode === 'chat' ? await createChat([...history, { role: 'user', content }], state.sessionId) : await createDashboardDraft({ operation, instruction: content, folderUid: folderUid.trim(), datasourceUids: datasourceUids.split(',').map((v) => v.trim()).filter(Boolean), dashboardUid: dashboardUid.trim() || undefined });
      dispatch({ type: 'accepted', requestId: command.requestId, sessionId: command.sessionId, model: command.model });
      subscriptionRef.current = subscribeChat(command).subscribe({
        next: (liveEvent) => {
          if (liveEvent.kind === 'message') {
            dispatch({ type: 'event', event: liveEvent.event });
            if (liveEvent.event.type === 'draft') { void getDashboardDraft(liveEvent.event.draftId).then(setDraft).catch(() => dispatch({ type: 'failed', message: '草稿已產生，但讀取預覽失敗。' })); }
          } else if (liveEvent.state === LiveChannelConnectionState.Disconnected || liveEvent.state === LiveChannelConnectionState.Shutdown) { dispatch({ type: 'connection', connection: 'disconnected' }); }
        },
        error: () => dispatch({ type: 'failed', message: 'Grafana Live 連線失敗，請重試。' }),
      });
    } catch (error) { dispatch({ type: 'failed', message: error instanceof Error ? error.message : '無法建立請求。' }); }
  }

  async function cancel() { if (!state.requestId) { return; } subscriptionRef.current?.unsubscribe(); try { await cancelChat(state.requestId); } finally { dispatch({ type: 'cancelled' }); } }
  async function applyDraft() {
    if (!draft || draft.applied) { return; }
    setApplying(true);
    try { setDraft(await applyDashboardDraft(draft.draftId, draft.revision)); }
    catch { dispatch({ type: 'failed', message: '套用 dashboard 草稿失敗，Grafana 未被修改。' }); }
    finally { setApplying(false); }
  }

  return <PluginPage><div className={styles.page}>
    <header className={styles.header}><div><h1>AI 助理</h1><div className={styles.model}>模型：{state.model}</div></div><Badge text={badge.text} color={badge.color} /></header>
    <div className={styles.tabs}><Button variant={mode === 'chat' ? 'primary' : 'secondary'} onClick={() => setMode('chat')}>對話</Button><Button variant={mode === 'dashboard' ? 'primary' : 'secondary'} onClick={() => setMode('dashboard')}>產生 Dashboard</Button></div>
    {mode === 'dashboard' && <section className={styles.settings}>
      <div className={styles.tabs}><Button size="sm" variant={operation === 'create_dashboard' ? 'primary' : 'secondary'} onClick={() => setOperation('create_dashboard')}>新增</Button><Button size="sm" variant={operation === 'modify_dashboard' ? 'primary' : 'secondary'} onClick={() => setOperation('modify_dashboard')}>修改</Button></div>
      <Field label="Datasource UID（逗號分隔）" required><Input value={datasourceUids} onChange={(e) => setDatasourceUids(e.currentTarget.value)} placeholder="prometheus" /></Field>
      <Field label="Folder UID（可留空）"><Input value={folderUid} onChange={(e) => setFolderUid(e.currentTarget.value)} /></Field>
      {operation === 'modify_dashboard' && <Field label="Dashboard UID" required><Input value={dashboardUid} onChange={(e) => setDashboardUid(e.currentTarget.value)} /></Field>}
    </section>}
    <section className={styles.conversation} aria-live="polite">
      {state.messages.length === 0 && <Alert title="純地端 AI 助理" severity="info">命令只送往 Grafana backend；聊天與 dashboard 結果皆透過 Grafana Live 串流。</Alert>}
      {state.messages.map((message) => <article key={message.id} className={message.role === 'user' ? styles.userMessage : styles.assistantMessage}><strong>{message.role === 'user' ? '你' : 'AI'}</strong><div className={styles.content}>{message.content || (message.status === 'pending' ? (mode === 'dashboard' ? '正在產生可審核草稿…' : '準備串流…') : '')}</div>{message.error && <div className={styles.error}>{message.error}</div>}</article>)}
      {draft && <article className={styles.draft}><h3>{draft.title}</h3><p>{draft.dashboard.panels instanceof Array ? `${draft.dashboard.panels.length} 個 panels` : 'Dashboard 草稿'}</p>{draft.warnings.map((w) => <Alert key={w.code} title={w.code} severity="warning">{w.message}</Alert>)}<details><summary>檢視 JSON</summary><pre>{JSON.stringify(draft.dashboard, null, 2)}</pre></details><Button onClick={applyDraft} disabled={applying || draft.applied}>{draft.applied ? '已套用至 Grafana' : applying ? '套用中…' : '確認並套用'}</Button></article>}
    </section>
    <form onSubmit={send} className={styles.composer}><Field label={mode === 'chat' ? '輸入訊息' : '描述要建立的 Dashboard'}><TextArea rows={4} value={input} disabled={active} onChange={(e) => setInput(e.currentTarget.value)} placeholder={mode === 'chat' ? '例如：說明目前服務的錯誤率趨勢' : '例如：建立 API latency 與錯誤率 dashboard'} /></Field><div className={styles.actions}><Button type="submit" disabled={!input.trim() || active || (mode === 'dashboard' && !datasourceUids.trim())}>送出</Button>{active && <Button type="button" variant="destructive" fill="outline" onClick={cancel}>取消</Button>}</div></form>
  </div></PluginPage>;
}

const getStyles = (theme: GrafanaTheme2) => ({
  page: css`max-width: 960px; margin: 0 auto; display: flex; flex-direction: column; gap: ${theme.spacing(2)};`, header: css`display: flex; justify-content: space-between; align-items: center; h1 { margin: 0; }`, model: css`color: ${theme.colors.text.secondary};`, tabs: css`display: flex; gap: ${theme.spacing(1)};`,
  settings: css`display: grid; gap: ${theme.spacing(1)}; padding: ${theme.spacing(2)}; border: 1px solid ${theme.colors.border.weak};`, conversation: css`min-height: 320px; display: flex; flex-direction: column; gap: ${theme.spacing(1.5)};`,
  userMessage: css`align-self: flex-end; max-width: 80%; padding: ${theme.spacing(1.5)}; border-radius: ${theme.shape.radius.default}; background: ${theme.colors.primary.transparent};`, assistantMessage: css`padding: ${theme.spacing(1.5)}; border: 1px solid ${theme.colors.border.weak}; background: ${theme.colors.background.secondary};`,
  content: css`white-space: pre-wrap; overflow-wrap: anywhere;`, error: css`color: ${theme.colors.error.text};`, draft: css`padding: ${theme.spacing(2)}; border: 1px solid ${theme.colors.success.border}; pre { max-height: 400px; overflow: auto; }`, composer: css`position: sticky; bottom: 0; padding: ${theme.spacing(2)} 0; background: ${theme.colors.background.primary};`, actions: css`display: flex; gap: ${theme.spacing(1)};`,
});
