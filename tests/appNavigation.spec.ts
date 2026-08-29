import { test, expect } from './fixtures';
import { ROUTES } from '../src/constants';

test.describe('AI Assistant app', () => {
  test('renders the assistant page from the app route', async ({ gotoPage, page }) => {
    await gotoPage(`/${ROUTES.Assistant}`);
    await expect(page.getByRole('heading', { name: 'AI 助理' })).toBeVisible();
    await expect(page.getByText('命令只送往 Grafana backend')).toBeVisible();
    await expect(page.getByRole('textbox', { name: '輸入訊息' })).toBeVisible();
    await expect(page.getByText(/11434|LLM_BASE_URL|API key/i)).toHaveCount(0);
  });

  test('shows dashboard mode without exposing provider configuration', async ({ gotoPage, page }) => {
    await gotoPage(`/${ROUTES.Assistant}`);
    await page.getByRole('button', { name: '產生 Dashboard' }).click();
    await expect(page.getByRole('textbox', { name: /Datasource UID/ })).toBeVisible();
    await expect(page.getByRole('textbox', { name: '描述要建立的 Dashboard' })).toBeVisible();
  });

  test('streams a local model response through Grafana Live', async ({ gotoPage, page }) => {
    await gotoPage(`/${ROUTES.Assistant}`);
    const prompt = page.getByRole('textbox', { name: '輸入訊息' });
    const send = page.getByRole('button', { name: '送出' });

    await prompt.fill('請只回覆：OK');
    await send.click();
    await expect(send).toBeDisabled();

    const assistantMessage = page.locator('article').filter({ hasText: /^AI/ }).last();
    await expect.poll(async () => (await assistantMessage.textContent())?.replace(/^AI/, '').trim().length ?? 0, {
      timeout: 120_000,
    }).toBeGreaterThan(0);
    await expect(send).toBeEnabled({ timeout: 120_000 });
    await expect(page.getByText('模型：qwen2.5:0.5b')).toBeVisible();
  });
});
