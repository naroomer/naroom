// 042_free_renewal_e2e.js — API E2E: free renewal eligibility, atomic guard, invoice invariant
//
// This is an API-level E2E test. It calls the backend directly via HTTP and injects DB state.
// For the browser-level UI test (Playwright), see 043_browser_renewal.js.
//
// Covers:
//   T1: listing older than 30 days with count=0 can still renew (no 30-day cutoff)
//   T2: early renewal (active, >1h left) returns 409
//   T3: expired listing + owner wallet + free renewal → 200
//   T4: renewed listing appears on board
//   T5: duplicate immediate renewal returns 409
//   T6: renewal_count increments; opened_chats_count unchanged
//   T7: renewal creates zero new invoices (DB row count before == after)
//   T8: count=2 listing renewal returns 409
//   T9: wrong wallet returns 403

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT  = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const CLIENT2 = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 042: Free Renewal E2E (expired listing → auth → renew → board) ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('042_free_renewal_e2e');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT, 'BTC', 'client');
    await api.verifyWallet(CLIENT2, 'BTC', 'client');

    // ── Setup: create a listing and wait for it to become active ─────────────

    let listingId;

    await t.run('T1-setup: client creates listing', async () => {
      const r = await api.createListing(CLIENT);
      assertStatus(r, 201, 'createListing');
      listingId = r.body.listing_id;
    });

    await t.run('T1-setup: listing becomes active', async () => {
      await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    // ── T1: listing older than 30 days renews OK ──────────────────────────────

    await t.run('T1: listing age >30 days does not block renewal (expire it first)', async () => {
      // Age the listing by 60 days and mark it expired
      srv.db(`UPDATE listings SET first_activated_at = strftime('%s','now') - 60*86400,
              visible_until = strftime('%s','now') - 100, status = 'expired'
              WHERE id = '${listingId}'`);
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      assertStatus(r, 200, 'renew 60-day-old listing');
      if (r.body.status !== 'renewed') throw new Error(`expected status=renewed, got ${r.body.status}`);
      if (r.body.free !== true) throw new Error(`expected free=true, got ${r.body.free}`);
    });

    // ── T2: early renewal blocked when >1h left ───────────────────────────────

    await t.run('T2: early renewal with >1h left returns 409', async () => {
      // Listing was just renewed (visible_until = now+86400). Try again immediately.
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      if (r.status !== 409) throw new Error(`expected 409 early renewal, got ${r.status}`);
    });

    // ── T3: expired listing + owner auth + free renewal returns 200 ───────────

    await t.run('T3: expire listing again and renew as owner', async () => {
      srv.db(`UPDATE listings SET visible_until = strftime('%s','now') - 1, status = 'expired'
              WHERE id = '${listingId}'`);
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      assertStatus(r, 200, 'renew expired listing');
      if (r.body.free !== true) throw new Error('expected free=true');
    });

    // ── T4: renewed listing appears on board ──────────────────────────────────

    await t.run('T4: renewed listing visible on board', async () => {
      const r = await api.get('/board/new_york');
      assertStatus(r, 200, 'GET /board');
      const ids = r.body.map(l => l.id);
      if (!ids.includes(listingId)) throw new Error(`listing ${listingId} not found on board after renewal`);
    });

    // ── T5: duplicate immediate renewal returns 409 ───────────────────────────

    await t.run('T5: duplicate immediate renewal returns 409', async () => {
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      if (r.status !== 409) throw new Error(`expected 409 duplicate, got ${r.status}`);
    });

    // ── T6: renewal_count incremented once; opened_chats_count unchanged ──────

    await t.run('T6: GET /listing confirms renewal_count and opened_chats_count', async () => {
      const r = await api.getListing(listingId);
      assertStatus(r, 200, 'getListing');
      // Two successful renewals in T1 and T3 → renewal_count = 2
      if (r.body.renewal_count !== 2) throw new Error(`expected renewal_count=2, got ${r.body.renewal_count}`);
      if (r.body.opened_chats_count !== 0) throw new Error(`opened_chats_count should still be 0, got ${r.body.opened_chats_count}`);
    });

    // ── T7: renewal creates zero new invoices (DB count before == count after) ─

    await t.run('T7: DB invoice count unchanged after renewal (renewal is free, no invoice row created)', async () => {
      // Record invoice count before renewal
      const before = parseInt(srv.db(`SELECT COUNT(*) FROM invoices`), 10);

      // Expire the listing so renewal is eligible (listing is active with 24h from T3)
      srv.db(`UPDATE listings SET visible_until = strftime('%s','now') - 1, status = 'expired'
              WHERE id = '${listingId}'`);
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      assertStatus(r, 200, 'T7 renew');
      if (r.body.free !== true) throw new Error(`expected free=true in T7 renewal`);

      // Count invoices after renewal
      const after = parseInt(srv.db(`SELECT COUNT(*) FROM invoices`), 10);
      if (after !== before) {
        throw new Error(`renewal must not create invoices: before=${before}, after=${after}`);
      }
    });

    // ── T8: count=2 listing renewal returns 409 ───────────────────────────────

    await t.run('T8: count=2 listing renewal returns 409', async () => {
      srv.db(`UPDATE listings SET opened_chats_count = 2, status = 'expired' WHERE id = '${listingId}'`);
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      if (r.status !== 409) throw new Error(`expected 409 (count=2), got ${r.status}`);
    });

    // ── T9: wrong wallet returns 403 ──────────────────────────────────────────

    await t.run('T9: wrong wallet returns 403', async () => {
      // Reset count so the listing is technically renewable
      srv.db(`UPDATE listings SET opened_chats_count = 0, status = 'expired',
              visible_until = strftime('%s','now') - 1 WHERE id = '${listingId}'`);
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT2);
      if (r.status !== 403) throw new Error(`expected 403 wrong wallet, got ${r.status}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
