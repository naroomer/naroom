// lib/server.js — start/stop the Go backend on an isolated port with a temp DB
import { spawn, execFileSync } from 'child_process';
import { mkdtempSync, rmSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';
import net from 'net';
import { createHmac, createHash, createCipheriv, randomBytes } from 'crypto';

const BACKEND_DIR = new URL('../../', import.meta.url).pathname.replace(/\/$/, '');

// Test-fixed values — must match env vars passed to TestServer
const TEST_SALT    = 'e2e-test-salt';
const TEST_ENC_KEY = 'e2e-test-wallet-enc-key-32bytes!';

export function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

export async function findFreePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

export async function assertPortClosed(port, timeout = 3000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const free = await new Promise(resolve => {
      const c = net.createConnection(port, '127.0.0.1');
      c.on('connect', () => { c.destroy(); resolve(false); });
      c.on('error', () => resolve(true));
    });
    if (free) return;
    await sleep(100);
  }
  throw new Error(`Port ${port} still in use after teardown`);
}

// Mirror of Go crypto.WalletHash: HMAC-SHA256(salt, "naroom:v1:" + normalizedAddress)
function walletHash(address) {
  const addr = address.trim();
  const lower = addr.toLowerCase();
  const normalized = (lower.startsWith('bc1') || lower.startsWith('ltc1')) ? lower : addr;
  return createHmac('sha256', Buffer.from(TEST_SALT))
    .update('naroom:v1:' + normalized)
    .digest('hex');
}

// Mirror of Go crypto.EncryptAddress: AES-256-GCM, nonce||ciphertext||tag, base64url
function encryptAddress(address) {
  const keyBytes = createHash('sha256').update(TEST_ENC_KEY).digest();
  const nonce = randomBytes(12);
  const cipher = createCipheriv('aes-256-gcm', keyBytes, nonce);
  const ct = Buffer.concat([cipher.update(address, 'utf8'), cipher.final()]);
  const tag = cipher.getAuthTag();
  return Buffer.concat([nonce, ct, tag]).toString('base64url');
}

export class TestServer {
  constructor({ devMode = true, extraEnv = {} } = {}) {
    this.port = null;
    this.dbPath = null;
    this.proc = null;
    this.tmpDir = null;
    this.base = null;
    this.wsBase = null;
    this._devMode = devMode;
    this._extraEnv = extraEnv;
  }

  async start() {
    this.port = await findFreePort();
    this.tmpDir = mkdtempSync(join(tmpdir(), 'naroom-e2e-'));
    this.dbPath = join(this.tmpDir, 'naroom.db');
    this.base = `http://127.0.0.1:${this.port}`;
    this.wsBase = `ws://127.0.0.1:${this.port}`;

    // Use -tags dev so DEV_MODE=true is accepted by the binary
    this.proc = spawn('go', ['run', '-tags', 'dev', './cmd/naroom/main.go'], {
      cwd: BACKEND_DIR,
      env: {
        ...process.env,
        DEV_MODE: this._devMode ? 'true' : 'false',
        SERVER_SALT: TEST_SALT,
        // Pin HASH_KEY explicitly — prevents parent env from overriding SERVER_SALT fallback.
        HASH_KEY: TEST_SALT,
        // Required in prod mode (devMode=false). Provide a fixed 32-byte test key.
        WALLET_ENC_KEY: TEST_ENC_KEY,
        PORT: String(this.port),
        DB_PATH: this.dbPath,
        TTL_CLEAN_INTERVAL: '5',       // fast cleanup for tests
        INVOICE_WATCH_INTERVAL: '2',   // fast invoice confirm for tests (default 30s is too slow)
        // Allow callers to override any env var (e.g. MEMPOOL_API, DEV_SKIP_PAYMENTS)
        ...this._extraEnv,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    // Uncomment for debugging:
    // this.proc.stdout.on('data', d => process.stdout.write(d));
    // this.proc.stderr.on('data', d => process.stderr.write(d));

    // Wait for /health
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
      try {
        const r = await fetch(`${this.base}/health`);
        if (r.ok) return this;
      } catch {}
      await sleep(250);
    }
    throw new Error('Backend failed to start in 15s');
  }

  async stop() {
    if (this.proc) {
      this.proc.kill('SIGKILL');
      this.proc = null;
      await sleep(300);
    }
    if (this.tmpDir) {
      try { rmSync(this.tmpDir, { recursive: true, force: true }); } catch {}
      this.tmpDir = null;
    }
    if (this.port) {
      await assertPortClosed(this.port).catch(e => console.warn('  warning:', e.message));
      this.port = null;
    }
  }

  db(sql) {
    return execFileSync('sqlite3', [this.dbPath, sql], { encoding: 'utf8' }).trim();
  }

  // registerDirect injects a wallet session and session token directly into the DB,
  // bypassing the /wallet/register API (and thus the blockchain balance check).
  // Used by tests that run in devMode=false but need registered wallets without real API calls.
  // Returns the raw session token (ready to use as Bearer token).
  registerDirect(address, role, currency = 'BTC', minRequiredUSD = null) {
    const now = Math.floor(Date.now() / 1000);
    const hash = walletHash(address);
    const enc  = encryptAddress(address);
    const minReq = minRequiredUSD !== null ? minRequiredUSD : (role === 'peer' ? 1000 : 150);

    this.db(
      `INSERT OR REPLACE INTO wallet_sessions ` +
      `(wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, balance_usd, last_checked_at, verified, first_seen, created_at) ` +
      `VALUES ('${hash}', '${enc}', '${currency}', '${role}', 'ok', ${minReq}, ${minReq}, ${now}, 1, ${now}, ${now})`
    );

    if (role === 'peer') {
      this.db(
        `INSERT OR IGNORE INTO reputation (counselor_hash, region, first_seen) ` +
        `VALUES ('${hash}', '', ${now})`
      );
    }

    const rawToken = randomBytes(32).toString('base64url');
    const tokenHash = createHash('sha256').update(rawToken).digest('hex');
    this.db(
      `INSERT INTO sessions (token_hash, wallet_hash, currency, role, created_at, expires_at) ` +
      `VALUES ('${tokenHash}', '${hash}', '${currency}', '${role}', ${now}, ${now + 86400})`
    );

    return rawToken;
  }
}
