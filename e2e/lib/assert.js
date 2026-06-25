// lib/assert.js — assertion helpers
import { execFileSync } from 'child_process';

export function assert(cond, msg) {
  if (!cond) throw new Error(`ASSERT FAILED: ${msg}`);
}

export function assertEqual(a, b, msg) {
  if (a !== b) throw new Error(`ASSERT FAILED: ${msg} — expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}`);
}

export function assertStatus(res, expected, label) {
  if (res.status !== expected) {
    throw new Error(`ASSERT FAILED: ${label} — expected HTTP ${expected}, got ${res.status}: ${JSON.stringify(res.body)}`);
  }
}

export function assertHasField(obj, field, label) {
  if (!obj[field]) throw new Error(`ASSERT FAILED: ${label} — missing field "${field}" in ${JSON.stringify(obj)}`);
}

export function assertNoField(obj, field, label) {
  if (obj[field] !== undefined && obj[field] !== null && obj[field] !== '') {
    throw new Error(`ASSERT FAILED: ${label} — field "${field}" should NOT be present, got ${JSON.stringify(obj[field])}`);
  }
}

// Poll until predicate returns truthy, with timeout
export async function pollUntil(fn, { timeout = 45000, interval = 2000, label = 'condition' } = {}) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await fn();
    if (last) return last;
    await sleep(interval);
  }
  throw new Error(`TIMEOUT: "${label}" not met within ${timeout}ms. Last: ${JSON.stringify(last)}`);
}

// Assert room is NOT visible to actor before expected phase (peerWallet has a verified session)
export async function assertNoRoom(api, peerWallet, listingId, label) {
  const r = await api.getPeerChatroom(peerWallet, listingId);
  if (r.status === 200 && r.body.room_id) {
    throw new Error(`ASSERT FAILED: ${label} — peer should NOT see a room yet, got ${JSON.stringify(r.body)}`);
  }
}

// Assert SQLite DB state directly
export function dbQuery(dbPath, sql) {
  return execFileSync('sqlite3', [dbPath, sql], { encoding: 'utf8' }).trim();
}

export function assertDbCount(dbPath, sql, expected, label) {
  const result = dbQuery(dbPath, sql);
  const count = parseInt(result, 10);
  if (count !== expected) {
    throw new Error(`ASSERT FAILED DB: ${label} — expected ${expected}, got ${count}. SQL: ${sql}`);
  }
}

export function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

export function log(label, msg) {
  console.log(`  [${label}] ${msg}`);
}

export function pass(msg) {
  console.log(`  ✓ ${msg}`);
}

export function fail(msg) {
  console.error(`  ✗ ${msg}`);
}
