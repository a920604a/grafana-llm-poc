import { getBackendSrv } from '@grafana/runtime';
import { ChatCommandResponse } from './contracts';

const resource = '/api/plugins/acme-aiassistant-app/resources';
export type DashboardOperation = 'create_dashboard' | 'modify_dashboard';
export interface DashboardDraft {
  draftId: string; revision: number; operation: DashboardOperation; title: string; folderUid: string;
  targetDashboardUid?: string; dashboard: Record<string, unknown>; warnings: Array<{ code: string; message: string }>;
  expiresAt: string; applied: boolean; applyResult?: Record<string, unknown>;
}
export interface DashboardDraftCommand { operation: DashboardOperation; instruction: string; folderUid: string; datasourceUids: string[]; dashboardUid?: string; }

export async function createDashboardDraft(command: DashboardDraftCommand): Promise<ChatCommandResponse> {
  return getBackendSrv().post(`${resource}/v1/dashboard-drafts`, command);
}
export async function getDashboardDraft(id: string): Promise<DashboardDraft> {
  return getBackendSrv().get(`${resource}/v1/dashboard-drafts/${encodeURIComponent(id)}`);
}
export async function applyDashboardDraft(id: string, revision: number): Promise<DashboardDraft> {
  return getBackendSrv().post(`${resource}/v1/dashboard-drafts/${encodeURIComponent(id)}/apply`, { revision, confirm: true });
}
