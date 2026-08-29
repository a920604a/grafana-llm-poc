import React from 'react';
import { AppPluginMeta, PluginConfigPageProps } from '@grafana/data';
import { Alert, FieldSet } from '@grafana/ui';

export interface AppConfigProps extends PluginConfigPageProps<AppPluginMeta> {}

const AppConfig = ({ plugin }: AppConfigProps) => (
  <FieldSet label="AI Assistant 設定">
    <Alert title="Server-managed configuration" severity={plugin.meta.enabled ? 'success' : 'warning'}>
      LLM endpoint、model 與 credentials 由 Grafana backend 的環境變數管理，不會提供給瀏覽器或儲存在 jsonData。
    </Alert>
    <dl>
      <dt>Plugin 狀態</dt>
      <dd>{plugin.meta.enabled ? '已啟用' : '未啟用'}</dd>
      <dt>Runtime</dt>
      <dd>OpenAI-compatible self-hosted provider</dd>
    </dl>
  </FieldSet>
);

export default AppConfig;
