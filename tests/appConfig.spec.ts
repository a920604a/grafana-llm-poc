import { test, expect } from './fixtures';

test('configuration is server-managed and exposes no credential fields', async ({ appConfigPage: _, page }) => {
  await expect(page.getByRole('group', { name: 'AI Assistant 設定' })).toBeVisible();
  await expect(page.getByText(/LLM endpoint、model 與 credentials/)).toBeVisible();
  await expect(page.getByRole('textbox')).toHaveCount(0);
});
