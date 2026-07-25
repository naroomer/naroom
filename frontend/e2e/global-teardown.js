// e2e/global-teardown.js — kill server process groups, verify ports closed, clean up artifacts.
//
// Teardown sequence:
//   1. SIGTERM both process groups → await exit (5s).
//   2. SIGKILL if still alive → await exit (3s) → THROW if still alive.
//   3. Verify both ports closed → THROW if still open.
//   4. Remove run temp directory.
//   5. Remove generated artifacts: frontend/test-results/, playwright-report/, root naroom.db.

import { createServer } from 'net';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

// Import shared utilities from global-setup.
import { terminateGroup, waitForPortClosed, readState } from './global-setup.js';

export default async function globalTeardown() {
  const runDir = process.env.PW_RUN_DIR;
  if (!runDir) {
    console.log('[teardown] PW_RUN_DIR not set; nothing to clean up.');
    return;
  }

  const sf = path.join(runDir, 'state.json');
  if (!fs.existsSync(sf)) {
    console.log('[teardown] No state file; nothing to clean up.');
    // Still remove run dir if it exists.
    try { fs.rmSync(runDir, { recursive: true, force: true }); } catch {}
    return;
  }

  let state;
  try {
    state = JSON.parse(fs.readFileSync(sf, 'utf-8'));
  } catch {
    console.log('[teardown] Could not read state file.');
    return;
  }

  const { skPort, goPort, goPgid, skPgid } = state;

  // Step 1+2: Terminate both process groups. terminateGroup throws if SIGKILL fails.
  const errors = [];
  await Promise.all([
    terminateGroup(goPgid, 'go-server').catch((e) => errors.push(e)),
    terminateGroup(skPgid, 'sk-server').catch((e) => errors.push(e)),
  ]);

  // Step 3: Verify both ports closed. waitForPortClosed throws on timeout.
  await Promise.all([
    waitForPortClosed(goPort).catch((e) => errors.push(e)),
    waitForPortClosed(skPort).catch((e) => errors.push(e)),
  ]);

  if (errors.length === 0) {
    console.log(`[teardown] Ports ${goPort} and ${skPort} confirmed closed.`);
  }

  // Step 4: Remove run temp directory (binary, DB, state, logs all inside).
  if (errors.length === 0) {
    try {
      fs.rmSync(runDir, { recursive: true, force: true });
      console.log(`[teardown] Removed run dir: ${runDir}`);
    } catch (e) {
      errors.push(new Error(`Failed to remove run dir ${runDir}: ${e.message}`));
    }
  } else {
    console.log(`[teardown] Skipping run dir removal due to earlier errors.`);
  }

  // Step 5: Remove generated artifacts regardless of earlier errors.
  const frontendDir = path.resolve(fileURLToPath(import.meta.url), '../..');
  const repoRoot = path.resolve(frontendDir, '..');

  const artifacts = [
    path.join(frontendDir, 'test-results'),
    path.join(frontendDir, 'playwright-report'),
    path.join(repoRoot, 'naroom.db'),
  ];

  for (const p of artifacts) {
    try {
      if (fs.existsSync(p)) {
        fs.rmSync(p, { recursive: true, force: true });
        console.log(`[teardown] Removed artifact: ${p}`);
      }
    } catch (e) {
      console.error(`[teardown] Could not remove artifact ${p}: ${e.message}`);
    }
  }

  if (errors.length > 0) {
    throw new AggregateError(errors, `[teardown] ${errors.length} teardown error(s): ${errors.map(e => e.message).join('; ')}`);
  }

  console.log('[teardown] Complete.');
}
