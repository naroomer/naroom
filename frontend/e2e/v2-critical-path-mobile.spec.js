// e2e/v2-critical-path-mobile.spec.js — V2 critical path, Chromium mobile (Pixel 5).
//
// Reduced scope: create → payment (auto-confirm) → telegram → publish → board.
// Uses the same production-wired backend as the desktop spec.
// No /dev/* endpoints; payment via FakeAutoChain; Telegram via webhook.

import { test, expect } from '@playwright/test';

const GO_PORT = process.env.PW_GO_PORT;
const V2_API = GO_PORT ? `http://127.0.0.1:${GO_PORT}` : 'http://127.0.0.1:0';
const TG_SECRET = 'testwebhooksecret12345678901234';

async function simulateTelegramStart(request, token, chatId = 88001) {
  const payload = {
    update_id: 2000 + chatId,
    message: {
      message_id: 2,
      from: { id: chatId, is_bot: false, first_name: 'MobileUser', username: 'mobile_pw' },
      chat: { id: chatId, type: 'private' },
      date: Math.floor(Date.now() / 1000),
      text: `/start ${token}`,
      entities: [{ offset: 0, length: 7 + token.length, type: 'bot_command' }],
    },
  };
  const res = await request.post(`${V2_API}/api/v2/telegram/client/webhook`, {
    headers: {
      'Content-Type': 'application/json',
      'X-Telegram-Bot-Api-Secret-Token': TG_SECRET,
    },
    data: payload,
  });
  return res.ok();
}

async function waitForInvoiceConfirmed(request, mc, wallet, timeoutMs = 55_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const remaining = deadline - Date.now();
    if (remaining <= 0) break;
    const perReq = Math.min(8_000, remaining);
    try {
      // Promise.race guarantees per-request cancellation even if Playwright's
      // timeout option doesn't abort an in-flight request (observed in run 2).
      const res = await Promise.race([
        request.post(`${V2_API}/api/v2/client/payment-intents/restore`, {
          data: { management_code: mc, wallet_address: wallet },
          timeout: perReq,
        }),
        new Promise((_, reject) =>
          setTimeout(() => reject(new Error('request timeout')), perReq + 200)
        ),
      ]);
      if (res.ok()) {
        const body = await res.json();
        if (body.invoice?.status === 'confirmed') return body;
      }
    } catch (e) {
      // timeout or network error — retry
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('Invoice not confirmed within timeout');
}

async function waitForTelegramReady(request, mc, wallet, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await request.post(`${V2_API}/api/v2/client/telegram-links/status`, {
        data: { management_code: mc, wallet_address: wallet },
        timeout: 8_000,
      });
      if (res.ok()) {
        const body = await res.json();
        if (body.status === 'ready' || body.status === 'active') return body.status;
      }
    } catch (e) {
      // timeout or network error — retry
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('Telegram not ready within timeout');
}

test.describe('V2 critical path — mobile Chromium', () => {
  test.beforeAll(() => {
    if (!process.env.PW_GO_PORT) {
      throw new Error('PW_GO_PORT not set — run via "npx playwright test" so globalSetup runs first.');
    }
  });

  test('mobile: create → payment (auto) → telegram → publish → board', async ({ page, request }) => {
    test.setTimeout(120_000); // 55s poll + telegram + form + board well within 120s
    // Step 1: Navigate to new listing page.
    await page.goto('/v2/new');
    await expect(page.locator('h1, h2').first()).toBeVisible();

    const WALLET = 'bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq';
    await page.fill('input[type="text"]', WALLET);
    await page.locator('button.btn-primary').click();

    // Step 2: Code gate.
    await expect(page.locator('.code-text')).toBeVisible({ timeout: 10_000 });
    const mc = (await page.locator('.code-text').textContent()).trim();
    expect(mc).toBeTruthy();
    await page.locator('input[type="checkbox"]').check();
    await page.locator('button.btn-primary').click();

    // Step 3: Invoice auto-confirm.
    await expect(page.locator('.invoice-box')).toBeVisible({ timeout: 10_000 });
    await waitForInvoiceConfirmed(request, mc, WALLET);

    // Step 4: Balance — click Continue.
    await page.waitForFunction(
      () => !!document.querySelector('button.btn-primary'),
      { timeout: 15_000 }
    );
    const cont = page.locator('button.btn-primary').filter({ hasText: /continue/i });
    if (await cont.count() > 0) await cont.click();

    // Step 5: Telegram.
    await expect(page.locator('button.tg-btn')).toBeVisible({ timeout: 15_000 });
    await page.locator('button.tg-btn').click();

    const linkEl = page.locator('a.tg-btn[href]');
    await expect(linkEl).toBeVisible({ timeout: 10_000 });
    const botUrl = await linkEl.getAttribute('href');
    const token = new URL(botUrl).searchParams.get('start');
    expect(token).toBeTruthy();

    await simulateTelegramStart(request, token, 88001);
    await waitForTelegramReady(request, mc, WALLET);

    // Page may auto-advance from telegram → form step before .ok-badge is stable.
    const chipGroupMob = page.locator('.chip-group').first();
    const okBadgeMob = page.locator('.ok-badge');
    await Promise.race([
      expect(okBadgeMob).toBeVisible({ timeout: 15_000 }),
      expect(chipGroupMob).toBeVisible({ timeout: 15_000 }),
    ]);
    const contMob = page.locator('button.btn-primary').filter({ hasText: /continue/i });
    if (await okBadgeMob.isVisible() && await contMob.count() > 0) {
      await contMob.click();
    }

    // Step 6: Listing form.
    await expect(page.locator('.chip-group').first()).toBeVisible({ timeout: 10_000 });
    await page.locator('.chip').filter({ hasText: /alcohol/i }).first().click();
    await page.locator('.chip').filter({ hasText: /crisis/i }).click();
    await page.locator('.chip').filter({ hasText: /urgent/i }).click();
    await page.locator('.chip').filter({ hasText: /^EN$/ }).click();
    await page.locator('input.contact-val').fill('@testuser_mobile_pw');
    await page.locator('button.btn-primary').filter({ hasText: /publish/i }).click();

    // Step 7: Done.
    await expect(page.locator('.done-icon')).toBeVisible({ timeout: 10_000 });

    // Step 8: Board.
    await page.goto('/v2/board/tbilisi');
    await expect(page.locator('.grid, .card').first()).toBeVisible({ timeout: 10_000 });
    expect(page.url()).not.toContain('/error');
  });
});
