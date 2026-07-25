// e2e/global-setup.js — builds Go binary, starts test servers, writes state to run dir.
//
// Must be invoked via `node e2e/pw.js` so that PW_SK_PORT / PW_GO_PORT / PW_RUN_DIR
// are already set in the environment before playwright.config.js is evaluated.
// Running `npx playwright test` directly (without the wrapper) throws here.
//
// Exports restartServers() for use by the critical-path spec to implement
// the restart/restore steps without /dev/* domain shortcuts.

import { execSync, spawn } from 'child_process';
import { connect, createServer } from 'net';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

// ── Port utilities ────────────────────────────────────────────────────────────

export async function waitForPort(port, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const up = await new Promise((resolve) => {
      const s = connect(port, '127.0.0.1');
      s.once('connect', () => { s.destroy(); resolve(true); });
      s.once('error', () => resolve(false));
    });
    if (up) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`Timeout waiting for port ${port} to open`);
}

export async function waitForPortClosed(port, timeoutMs = 10_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const closed = await new Promise((resolve) => {
      const srv = createServer();
      srv.once('listening', () => { srv.close(() => resolve(true)); });
      srv.once('error', () => resolve(false));
      srv.listen(port, '127.0.0.1');
    });
    if (closed) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`Port ${port} still open after ${timeoutMs}ms — teardown incomplete`);
}

// ── Process group utilities ───────────────────────────────────────────────────

function killGroup(pgid, sig) {
  try { process.kill(-pgid, sig); return true; }
  catch { return false; }
}

function groupGone(pgid) {
  try { process.kill(-pgid, 0); return false; }
  catch { return true; }
}

async function waitGroupExit(pgid, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (groupGone(pgid)) return true;
    await new Promise((r) => setTimeout(r, 100));
  }
  return false;
}

export async function terminateGroup(pgid, label = String(pgid)) {
  if (groupGone(pgid)) {
    console.log(`[harness] ${label} PGID=${pgid} already gone`);
    return;
  }
  killGroup(pgid, 'SIGTERM');
  console.log(`[harness] SIGTERM → ${label} PGID=${pgid}`);
  if (await waitGroupExit(pgid, 5_000)) {
    console.log(`[harness] ${label} PGID=${pgid} exited after SIGTERM`);
    return;
  }
  killGroup(pgid, 'SIGKILL');
  console.log(`[harness] SIGKILL → ${label} PGID=${pgid}`);
  if (!await waitGroupExit(pgid, 3_000)) {
    throw new Error(`[harness] ${label} PGID=${pgid} did NOT exit after SIGKILL — teardown failed`);
  }
  console.log(`[harness] ${label} PGID=${pgid} exited after SIGKILL`);
}

// ── State file ────────────────────────────────────────────────────────────────

function stateFilePath() {
  const runDir = process.env.PW_RUN_DIR;
  if (!runDir) throw new Error('PW_RUN_DIR not set — run via `node e2e/pw.js`');
  return path.join(runDir, 'state.json');
}

export function readState() {
  return JSON.parse(fs.readFileSync(stateFilePath(), 'utf-8'));
}

function writeState(state) {
  fs.writeFileSync(stateFilePath(), JSON.stringify(state, null, 2));
}

// ── Start one Go server process ────────────────────────────────────────────────

function startGoServer(binaryPath, goPort, dbPath, label = 'go') {
  const proc = spawn(binaryPath, [], {
    env: {
      ...process.env,
      V2TEST_PORT: String(goPort),
      V2TEST_DB_PATH: dbPath,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  proc.unref();
  proc.stdout.on('data', (c) => process.stdout.write(`[${label}] ` + c));
  proc.stderr.on('data', (c) => process.stderr.write(`[${label}] ` + c));
  proc.on('error', (e) => { throw e; });
  return proc;
}

// ── Start one SvelteKit server process ────────────────────────────────────────

function startSKServer(skPort, goPort, label = 'sk') {
  const frontendDir = path.resolve(fileURLToPath(import.meta.url), '../..');
  const proc = spawn('node', ['build'], {
    cwd: frontendDir,
    env: {
      ...process.env,
      PORT: String(skPort),
      V2_API_URL: `http://127.0.0.1:${goPort}`,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  proc.unref();
  proc.stdout.on('data', (c) => process.stdout.write(`[${label}] ` + c));
  proc.stderr.on('data', (c) => process.stderr.write(`[${label}] ` + c));
  proc.on('error', (e) => { throw e; });
  return proc;
}

// ── Global setup (called by Playwright once before all tests) ─────────────────

export default async function globalSetup() {
  const skPort = parseInt(process.env.PW_SK_PORT ?? '', 10);
  const goPort = parseInt(process.env.PW_GO_PORT ?? '', 10);
  const runDir = process.env.PW_RUN_DIR;
  if (!skPort || !goPort || !runDir) {
    throw new Error(
      'PW_SK_PORT / PW_GO_PORT / PW_RUN_DIR not set. Run tests via `node e2e/pw.js` or `npm test`.'
    );
  }

  // ── Build Go binary into run directory ────────────────────────────────────
  const repoRoot = path.resolve(fileURLToPath(import.meta.url), '../../..');
  const binaryPath = path.join(runDir, 'v2testserver');
  const dbPath = path.join(runDir, 'naroom.db');
  const sf = stateFilePath();

  console.log('[setup] Building Go test server binary...');
  execSync(
    `go build -o ${binaryPath} naroom/cmd/v2testserver`,
    { cwd: repoRoot, stdio: 'inherit', timeout: 120_000 }
  );
  console.log(`[setup] Binary: ${binaryPath}`);
  console.log(`[setup] DB:     ${dbPath}`);

  // ── Start Go test server ──────────────────────────────────────────────────
  const goProc = startGoServer(binaryPath, goPort, dbPath, 'go');
  await waitForPort(goPort, 30_000);

  // ── Start SvelteKit SSR server ────────────────────────────────────────────
  const skProc = startSKServer(skPort, goPort, 'sk');
  await waitForPort(skPort, 30_000);

  // ── Write state ───────────────────────────────────────────────────────────
  const state = {
    skPort,
    goPort,
    goPgid: goProc.pid,   // detached=true → child is its own process group leader
    skPgid: skProc.pid,
    binaryPath,
    dbPath,
    runDir,
  };
  writeState(state);

  console.log(
    `[setup] Ready — SK=${skPort} (PID=${skProc.pid} PGID=${skProc.pid}), ` +
    `Go=${goPort} (PID=${goProc.pid} PGID=${goProc.pid})`
  );
}

// ── restartServers — for use by critical-path spec ────────────────────────────
//
// Kills both process groups, waits for ports to close, restarts both servers
// with the SAME ports and DB path, waits for ports to come up, updates state.
// Returns the new state object.
export async function restartServers() {
  const state = readState();
  const { skPort, goPort, goPgid, skPgid, binaryPath, dbPath } = state;

  console.log(`[restart] Killing Go PGID=${goPgid} SK PGID=${skPgid}`);
  await Promise.all([
    terminateGroup(goPgid, 'go-server'),
    terminateGroup(skPgid, 'sk-server'),
  ]);

  console.log('[restart] Waiting for ports to close...');
  await Promise.all([
    waitForPortClosed(goPort),
    waitForPortClosed(skPort),
  ]);
  console.log(`[restart] Ports ${goPort} and ${skPort} closed.`);

  console.log('[restart] Starting new processes with same ports and DB...');
  const goProc = startGoServer(binaryPath, goPort, dbPath, 'go-r');
  const skProc = startSKServer(skPort, goPort, 'sk-r');

  await Promise.all([
    waitForPort(goPort, 30_000),
    waitForPort(skPort, 30_000),
  ]);

  const newState = {
    ...state,
    goPgid: goProc.pid,
    skPgid: skProc.pid,
  };
  writeState(newState);

  console.log(
    `[restart] New Go PID=${goProc.pid} PGID=${goProc.pid}, SK PID=${skProc.pid} PGID=${skProc.pid}`
  );
  return newState;
}
