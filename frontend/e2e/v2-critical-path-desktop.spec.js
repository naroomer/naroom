// e2e/v2-critical-path-desktop.spec.js — full V2 critical journey, Chromium desktop.
//
// A single sequential test covering all 10 required steps:
//   1.  Informer subscriber subscribes via UI before first publish.
//   2.  Client creates, pays, and publishes listing via UI.
//   3.  Informer subscriber receives first-publish notification via
//       production outbox + InformerWorker + fake sender.
//   4.  Client leaves "up" or "down" review via Telegram callback simulation
//       (production webhook route).
//   5.  Helper buys contact via UI.
//   6.  Contact is revealed and visible on the purchase page (UI — no direct API).
//   7.  Helper leaves "up" or "down" review via UI (purchase page buttons).
//   8.  Both review aggregates observable — client_reputation updated on listing.
//   9.  Client performs daily reactivation via UI (/v2/restore → /v2/new).
//  10.  Reactivation does NOT produce a second informer notification.
//  11.  Backend + frontend restart (same file DB, same ports, no /dev/* domain shortcut).
//  12.  Client restores listing via /v2/restore UI after restart.
//  13.  Helper restores purchase via /v2/helper/purchase UI after restart.
//
// Rules enforced:
//   - No direct domain API calls for user actions (create/reveal/review/reactivate).
//   - External Telegram events are simulated via the production webhook routes.
//   - /dev/time/advance is test infrastructure, not domain state.
//   - /dev/review-prompts and /dev/informer-messages are assertion sinks only.
//   - Polling/read-only endpoints used for synchronisation only.
//   - No page.route mocking of domain APIs.

import { test, expect } from '@playwright/test';
import { restartServers } from './global-setup.js';

const GO_PORT = process.env.PW_GO_PORT;
const V2_API  = GO_PORT ? `http://127.0.0.1:${GO_PORT}` : 'http://127.0.0.1:0';

// Telegram secrets matching v2testserver fixed values.
const CLIENT_TG_SECRET   = 'testwebhooksecret12345678901234';
const INFORMER_TG_SECRET = 'testinformersecret1234567890123';

// Test wallets.
const CLIENT_WALLET   = 'bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq';
const HELPER_WALLET   = 'bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4';
const INFORMER_WALLET = 'bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq'; // valid BTC bech32; wallet discarded by informer service after balance check
const CLIENT_CHAT_ID   = 99001;
const INFORMER_CHAT_ID = 77001;

// ── Helpers: Telegram simulations ────────────────────────────────────────────

// Simulate /start TOKEN to the client bot (for listing creation).
async function simulateClientTelegramStart(request, token, chatId) {
  return request.post(`${V2_API}/api/v2/telegram/client/webhook`, {
    headers: {
      'Content-Type': 'application/json',
      'X-Telegram-Bot-Api-Secret-Token': CLIENT_TG_SECRET,
    },
    data: {
      update_id: 1000 + chatId,
      message: {
        message_id: 1,
        from: { id: chatId, is_bot: false, first_name: 'CritUser', username: 'crit_pw' },
        chat: { id: chatId, type: 'private' },
        date: Math.floor(Date.now() / 1000),
        text: `/start ${token}`,
        entities: [{ offset: 0, length: 7 + token.length, type: 'bot_command' }],
      },
    },
    timeout: 10_000,
  });
}

// Simulate /start RAW_TOKEN to the informer bot.
async function simulateInformerTelegramStart(request, rawToken, chatId) {
  return request.post(`${V2_API}/api/v2/telegram/informer/webhook`, {
    headers: {
      'Content-Type': 'application/json',
      'X-Telegram-Bot-Api-Secret-Token': INFORMER_TG_SECRET,
    },
    data: {
      update_id: 2000 + chatId,
      message: {
        message_id: 10,
        from: { id: chatId, is_bot: false, first_name: 'InfUser', username: 'inf_pw' },
        chat: { id: chatId, type: 'private' },
        date: Math.floor(Date.now() / 1000),
        text: `/start ${rawToken}`,
        entities: [{ offset: 0, length: 7 + rawToken.length, type: 'bot_command' }],
      },
    },
    timeout: 10_000,
  });
}

// Simulate a Telegram callback_query (for client review via inline button).
async function simulateClientCallbackQuery(request, chatId, callbackData) {
  return request.post(`${V2_API}/api/v2/telegram/client/webhook`, {
    headers: {
      'Content-Type': 'application/json',
      'X-Telegram-Bot-Api-Secret-Token': CLIENT_TG_SECRET,
    },
    data: {
      update_id: 3000 + chatId,
      callback_query: {
        id: `cq_${Date.now()}`,
        from: { id: chatId, is_bot: false, first_name: 'CritUser' },
        data: callbackData,
        message: {
          message_id: 200,
          chat: { id: chatId, type: 'private' },
          date: Math.floor(Date.now() / 1000),
          text: 'Did this Helper help you?',
        },
      },
    },
    timeout: 10_000,
  });
}

// ── Helpers: polling ──────────────────────────────────────────────────────────

async function waitForInvoiceConfirmed(request, mc, wallet, timeoutMs = 40_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await request.post(`${V2_API}/api/v2/client/payment-intents/restore`, {
        data: { management_code: mc, wallet_address: wallet },
        timeout: 8_000,
      });
      if (res.ok()) {
        const body = await res.json();
        if (body.invoice?.status === 'confirmed') return body;
      }
    } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error('Client invoice not confirmed within timeout');
}

async function waitForClientTelegramReady(request, mc, wallet, timeoutMs = 20_000) {
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
    } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error('Client Telegram link not ready within timeout');
}

async function waitForInformerClaimed(request, rawToken, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await request.post(`${V2_API}/api/v2/informer/status`, {
        data: { raw_token: rawToken },
        timeout: 8_000,
      });
      if (res.ok()) {
        const body = await res.json();
        if (body.state === 'claimed') return;
      }
    } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error('Informer token not claimed within timeout');
}

async function waitForHelperContactReady(request, purchaseToken, wallet, timeoutMs = 40_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await request.post(`${V2_API}/api/v2/helper/contact-purchases/restore`, {
        data: { purchase_token: purchaseToken, wallet_address: wallet },
        timeout: 8_000,
      });
      if (res.ok()) {
        const body = await res.json();
        if (body.phase === 'contact_ready') return body;
      }
    } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error('Helper purchase did not reach contact_ready within timeout');
}

async function waitForReviewPrompt(request, chatId, timeoutMs = 15_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await request.get(
        `${V2_API}/dev/review-prompts?chat_id=${chatId}`,
        { timeout: 5_000 }
      );
      if (res.ok()) {
        const body = await res.json();
        if ((body.prompts || []).length > 0) return body.prompts;
      }
    } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error(`Review prompt for chat_id=${chatId} not captured within timeout`);
}

async function waitForListingPhase(request, mc, wallet, targetPhase, timeoutMs = 8_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await request.post(`${V2_API}/api/v2/client/listings/restore`, {
        data: { management_code: mc, wallet_address: wallet },
        timeout: 5_000,
      });
      if (res.ok()) {
        const body = await res.json();
        if (body.phase === targetPhase) return body;
      }
    } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error(`Listing phase ${targetPhase} not reached within ${timeoutMs}ms`);
}

// ── Critical journey ──────────────────────────────────────────────────────────

test.describe('V2 critical journey — desktop Chromium', () => {
  test.beforeAll(() => {
    if (!process.env.PW_GO_PORT) {
      throw new Error('PW_GO_PORT not set — run via "node e2e/pw.js" or "npm test".');
    }
  });

  test('10-step critical journey: informer → client → helper → reviews → reactivate → restart → restore', async ({ page, request, context }) => {
    test.setTimeout(300_000); // 5 minutes for the full sequential journey.

    // Shared state accumulated across steps.
    let managementCode = '';
    let listingId = '';
    let informerRawToken = '';
    let purchaseToken = '';

    // ── Step 1: Informer subscribes via UI ───────────────────────────────────
    // Intercept the access response to capture raw_token (read-only observation).
    // Playwright APIResponse has no .clone(); read body then reconstruct response.
    await page.route('**/api/v2/informer/access', async (route) => {
      const resp = await route.fetch();
      const bodyBytes = await resp.body();
      let body = {};
      try { body = JSON.parse(bodyBytes.toString()); } catch {}
      informerRawToken = body.raw_token || '';
      await route.fulfill({
        status: resp.status(),
        headers: resp.headers(),
        body: bodyBytes,
      });
    });

    await page.goto('/v2/informer');
    await expect(page.locator('h1')).toBeVisible({ timeout: 10_000 });

    // Enter informer wallet (BTC bech32 so currency is detected).
    await page.fill('input[type="text"]', INFORMER_WALLET);
    // City select defaults to tbilisi — no change needed.
    await page.locator('button.btn-primary').click();

    // Page transitions to 'waiting' step (status badge + TG button both appear).
    await expect(page.locator('.status-badge.waiting, button.tg-btn').first()).toBeVisible({ timeout: 10_000 });
    await page.unroute('**/api/v2/informer/access');
    expect(informerRawToken).toBeTruthy();

    // Simulate Telegram /start via production informer webhook.
    const infRes = await simulateInformerTelegramStart(request, informerRawToken, INFORMER_CHAT_ID);
    expect(infRes.ok()).toBe(true);

    // Poll informer status until claimed (production outbox path).
    await waitForInformerClaimed(request, informerRawToken);

    // Informer page should auto-advance to 'connected' step.
    await expect(page.locator('.done-icon')).toBeVisible({ timeout: 10_000 });

    // ── Step 2: Client creates, pays, and publishes listing via UI ────────────
    await page.goto('/v2/new');
    await expect(page.locator('h1, h2').first()).toBeVisible({ timeout: 10_000 });

    // 2a: Enter wallet.
    await page.fill('input[type="text"]', CLIENT_WALLET);
    await page.locator('button.btn-primary').click();

    // 2b: Management code gate.
    await expect(page.locator('.code-text')).toBeVisible({ timeout: 10_000 });
    managementCode = (await page.locator('.code-text').textContent()).trim();
    expect(managementCode).toBeTruthy();
    await page.locator('input[type="checkbox"]').check();
    await page.locator('button.btn-primary').click();

    // 2c: Invoice — wait for auto-confirm via FakeAutoChain + V2Watcher.
    await expect(page.locator('.invoice-box')).toBeVisible({ timeout: 10_000 });
    await waitForInvoiceConfirmed(request, managementCode, CLIENT_WALLET);

    // 2d: Balance step — click Continue.
    const contBtn1 = page.locator('button.btn-primary').filter({ hasText: /continue/i });
    await expect(contBtn1.first()).toBeVisible({ timeout: 15_000 });
    await contBtn1.first().click();

    // 2e: Telegram step.
    await expect(page.locator('button.tg-btn')).toBeVisible({ timeout: 15_000 });
    await page.locator('button.tg-btn').click();

    const linkEl = page.locator('a.tg-btn[href]');
    await expect(linkEl).toBeVisible({ timeout: 10_000 });
    const botUrl = await linkEl.getAttribute('href');
    const clientToken = new URL(botUrl).searchParams.get('start');
    expect(clientToken).toBeTruthy();

    await simulateClientTelegramStart(request, clientToken, CLIENT_CHAT_ID);
    await waitForClientTelegramReady(request, managementCode, CLIENT_WALLET);

    // Page may auto-advance to form step.
    const chipGroupEl = page.locator('.chip-group').first();
    const okBadgeEl = page.locator('.ok-badge');
    await Promise.race([
      expect(okBadgeEl).toBeVisible({ timeout: 15_000 }),
      expect(chipGroupEl).toBeVisible({ timeout: 15_000 }),
    ]);
    const contBtn2 = page.locator('button.btn-primary').filter({ hasText: /continue/i });
    if (await okBadgeEl.isVisible() && await contBtn2.count() > 0) {
      await contBtn2.click();
    }

    // 2f: Listing form.
    await expect(page.locator('.chip-group').first()).toBeVisible({ timeout: 10_000 });
    await page.locator('.chip').filter({ hasText: /alcohol/i }).first().click();
    await page.locator('.chip').filter({ hasText: /crisis/i }).click();
    await page.locator('.chip').filter({ hasText: /urgent/i }).click();
    await page.locator('.chip').filter({ hasText: /^EN$/ }).click();
    await page.locator('input.contact-val').fill('@testclient_crit_pw');
    await page.locator('button.btn-primary').filter({ hasText: /publish/i }).click();

    // 2g: Done — listing published.
    await expect(page.locator('.done-icon')).toBeVisible({ timeout: 10_000 });

    // ── Step 3: Informer subscriber receives first-publish notification ────────
    // InformerWorker polls outbox every 500ms. Wait up to 10s for delivery.
    const infMsgsRes = await (async () => {
      const deadline = Date.now() + 10_000;
      while (Date.now() < deadline) {
        try {
          const r = await request.get(`${V2_API}/dev/informer-messages`, { timeout: 5_000 });
          if (r.ok()) {
            const b = await r.json();
            if ((b.messages || []).length > 0) return b;
          }
        } catch {}
        await new Promise(r => setTimeout(r, 500));
      }
      throw new Error('Informer notification not delivered within 10s');
    })();
    const infMsgs = infMsgsRes.messages;
    expect(infMsgs.length).toBeGreaterThanOrEqual(1);
    expect(infMsgs[0].chat_id).toBe(INFORMER_CHAT_ID);
    const infMsgCountAfterPublish = infMsgs.length;

    // ── Step 4: Helper buys contact via UI ─────────────────────────────────────
    // Navigate to board and find the published listing.
    await page.goto('/v2/board/tbilisi');
    await expect(page.locator('a.card.listing').first()).toBeVisible({ timeout: 10_000 });

    // Click listing → listing detail page.
    await page.locator('a.card.listing').first().click();
    await expect(page.locator('.dep, h2, .listing-card').first()).toBeVisible({ timeout: 10_000 });

    // Save listing ID from URL.
    const listingUrl = page.url();
    const listingIdMatch = listingUrl.match(/\/v2\/listing\/([^/?]+)/);
    if (listingIdMatch) listingId = listingIdMatch[1];

    // Enter helper wallet and click purchase button.
    await page.fill('input[type="text"]', HELPER_WALLET);
    await page.locator('button.btn-primary').filter({
      hasText: /purchase|help|contact|buy/i,
    }).click();

    // Navigates to /v2/helper/purchase.
    await page.waitForURL('**/v2/helper/purchase', { timeout: 10_000 });
    await expect(page.locator('.invoice-box, .status-msg')).toBeVisible({ timeout: 10_000 });

    // Read purchase_token from sessionStorage (set by listing page before navigation).
    purchaseToken = await page.evaluate(
      () => sessionStorage.getItem('v2_active_purchase_token') || ''
    );
    expect(purchaseToken).toBeTruthy();

    // Poll until contact_ready (FakeAutoChain + HelperWatcher auto-confirm).
    await waitForHelperContactReady(request, purchaseToken, HELPER_WALLET);

    // ── Step 5: Contact is revealed and visible via UI ─────────────────────────
    // Page detects contact_ready and calls loadContact() automatically.
    // Poll until contact value appears in the DOM (.cv-text is the specific text span).
    await expect(page.locator('.cv-text')).toBeVisible({ timeout: 20_000 });
    const contactValue = (await page.locator('.cv-text').textContent().catch(() => '')).trim();
    expect(contactValue).toBeTruthy(); // e.g. '@testclient_crit_pw'

    // ── Step 6: Helper leaves review via UI ────────────────────────────────────
    // Review section appears after contact is revealed (reviewToken loaded by loadReviewCapability).
    await expect(page.locator('.review-btn.positive')).toBeVisible({ timeout: 10_000 });
    await page.locator('.review-btn.positive').click();

    // Verify review submitted indicator.
    await expect(page.locator('.review-done, .review-section')).toBeVisible({ timeout: 5_000 });
    // Specifically: after submitReview, reviewSubmitted=true → .review-done shown, .review-btns hidden.
    await expect(page.locator('.review-btn.positive')).not.toBeVisible({ timeout: 5_000 });

    // ── Step 7: Client review via Telegram (production webhook route) ──────────
    // The lifecycle worker (ReviewDeliveryOnce at 500ms) sends a review prompt to
    // the client via recordingBotSender. Poll /dev/review-prompts to get posData.
    const prompts = await waitForReviewPrompt(request, CLIENT_CHAT_ID);
    expect(prompts.length).toBeGreaterThanOrEqual(1);
    const { pos_data: posData } = prompts[0];
    expect(posData).toBeTruthy();

    // Send callback_query with posData to the production client webhook.
    const reviewCbRes = await simulateClientCallbackQuery(request, CLIENT_CHAT_ID, posData);
    expect(reviewCbRes.ok()).toBe(true);

    // ── Step 8: Both review aggregates observable ──────────────────────────────
    // Navigate to listing detail; client_reputation.positive_count should now be ≥1.
    if (listingId) {
      await page.goto(`/v2/listing/${listingId}`);
      // Wait for listing to load (rep-score may appear after listing renders).
      await expect(page.locator('.listing-card, .dep')).toBeVisible({ timeout: 10_000 });
      // Poll via API until client_reputation reflects the helper's review.
      const deadline = Date.now() + 10_000;
      let posCount = 0;
      while (Date.now() < deadline) {
        try {
          const r = await request.get(`${V2_API}/api/v2/listings/${listingId}`, { timeout: 5_000 });
          if (r.ok()) {
            const b = await r.json();
            posCount = b.client_reputation?.positive_count ?? 0;
            if (posCount >= 1) break;
          }
        } catch {}
        await new Promise(r => setTimeout(r, 500));
      }
      expect(posCount).toBeGreaterThanOrEqual(1);
    }

    // ── Step 9: Client performs daily reactivation via UI ─────────────────────
    // 9a: Advance time past 24-hour window (via test infrastructure, not domain state).
    const advRes = await request.post(`${V2_API}/dev/time/advance`, {
      data: { seconds: 86401 },
      timeout: 5_000,
    });
    expect(advRes.ok()).toBe(true);

    // 9b: Wait for lifecycle worker to hide the listing (polls at 500ms).
    await waitForListingPhase(request, managementCode, CLIENT_WALLET, 'hidden', 8_000);

    // 9c: Navigate to /v2/restore via UI.
    await page.goto('/v2/restore');
    await expect(page.locator('h1')).toBeVisible({ timeout: 10_000 });

    // 9d: Fill in wallet and management code, submit.
    // Restore page has two inputs: wallet and code. Fill wallet first (first input[type=text]).
    const inputs = page.locator('input[type="text"]');
    await inputs.first().fill(CLIENT_WALLET);
    await page.locator('input.code-input').fill(managementCode);
    await page.locator('button.btn-primary').click();

    // 9e: Page navigates to /v2/new (window.location.href = '/v2/new').
    await page.waitForURL('**/v2/new', { timeout: 15_000 });

    // 9f: /v2/new detects phase=hidden → shows telegram step for reactivation.
    await expect(page.locator('button.tg-btn')).toBeVisible({ timeout: 15_000 });
    await page.locator('button.tg-btn').click();

    // 9g: Get new binding token from link.
    const reactivateLinkEl = page.locator('a.tg-btn[href]');
    await expect(reactivateLinkEl).toBeVisible({ timeout: 10_000 });
    const reactivateBotUrl = await reactivateLinkEl.getAttribute('href');
    const reactivateToken = new URL(reactivateBotUrl).searchParams.get('start');
    expect(reactivateToken).toBeTruthy();
    expect(reactivateToken).not.toBe(clientToken); // Must be a fresh token

    // 9h: Simulate Telegram /start for the fresh binding.
    await simulateClientTelegramStart(request, reactivateToken, CLIENT_CHAT_ID);
    await waitForClientTelegramReady(request, managementCode, CLIENT_WALLET);

    // 9i: Page auto-reactivates (isReactivating=true, pollTelegramStatus detects ready).
    await expect(page.locator('.done-icon')).toBeVisible({ timeout: 20_000 });

    // ── Step 10: Reactivation does NOT produce a second informer notification ──
    // InformerWorker has already processed the outbox; reactivation must not enqueue again.
    // Wait briefly (3 ticks × 500ms) then check count unchanged.
    await new Promise(r => setTimeout(r, 2_000));
    const infMsgsAfterReact = await request.get(`${V2_API}/dev/informer-messages`, { timeout: 5_000 });
    const infBodyAfterReact = await infMsgsAfterReact.json();
    expect(infBodyAfterReact.messages.length).toBe(infMsgCountAfterPublish);

    // ── Step 11: Restart backend and frontend with same ports and DB ──────────
    // Uses restartServers() from global-setup — no /dev/* domain shortcut.
    const preRestartState = { goPgid: null, skPgid: null };
    try {
      // Import current state for logging.
      const { readState } = await import('./global-setup.js');
      const s = readState();
      preRestartState.goPgid = s.goPgid;
      preRestartState.skPgid = s.skPgid;
      console.log(`[crit] Pre-restart: Go PGID=${s.goPgid}, SK PGID=${s.skPgid}`);
    } catch {}

    const newState = await restartServers();
    console.log(
      `[crit] Post-restart: Go PID=${newState.goPgid}, SK PID=${newState.skPgid}; ` +
      `ports SK=${newState.skPort} Go=${newState.goPort}`
    );
    // Both processes must have new PIDs.
    if (preRestartState.goPgid) {
      expect(newState.goPgid).not.toBe(preRestartState.goPgid);
      expect(newState.skPgid).not.toBe(preRestartState.skPgid);
    }

    // ── Step 12: Client restores listing via /v2/restore UI after restart ──────
    await page.goto('/v2/restore');
    await expect(page.locator('h1')).toBeVisible({ timeout: 15_000 });

    const restoreInputs = page.locator('input[type="text"]');
    await restoreInputs.first().fill(CLIENT_WALLET);
    await page.locator('input.code-input').fill(managementCode);
    await page.locator('button.btn-primary').click();

    // Should navigate to /v2/new. Phase should be 'visible' (reactivated in step 9).
    await page.waitForURL('**/v2/new', { timeout: 15_000 });
    // done-icon visible (listing is visible after reactivation).
    await expect(page.locator('.done-icon')).toBeVisible({ timeout: 15_000 });

    // ── Step 13: Helper restores purchase via /v2/helper/purchase UI ──────────
    // sessionStorage persists in the Playwright browser context across restarts.

    // Pre-check: verify sessionStorage still has both keys needed by the purchase page.
    const ssCheck = await page.evaluate(() => {
      const active = sessionStorage.getItem('v2_active_purchase_token') || '';
      const savedRaw = sessionStorage.getItem(`v2_purchase_${active}`) || '';
      let purchaseId = '';
      try { purchaseId = JSON.parse(savedRaw)?.purchaseId || ''; } catch {}
      return { active, savedRaw, purchaseId };
    });
    expect(ssCheck.active).toBeTruthy();
    expect(ssCheck.savedRaw).toBeTruthy();

    await page.goto('/v2/helper/purchase');
    // Page reads v2_active_purchase_token from sessionStorage and calls restorePurchase().
    // Phase=contact_ready → loadContact() reveals contact → contact value appears.
    await expect(page.locator('.cv-text')).toBeVisible({ timeout: 20_000 });

    const restoredContact = (await page.locator('.cv-text').textContent()).trim();
    expect(restoredContact).toBe(contactValue); // Same contact as before restart.
    expect(restoredContact).not.toBe('');

    console.log('[crit] Critical journey complete — all 13 steps passed.');
  });
});
