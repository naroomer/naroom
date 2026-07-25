#!/usr/bin/env node
// e2e/pw.js — self-contained Playwright runner.
//
// Reserves two distinct dynamic TCP ports SIMULTANEOUSLY (both sockets bound at once),
// creates a unique mkdtemp run directory, then spawns Playwright with all resources set
// in the environment. A per-run JSON report is written to the run directory so that
// critical and smoke counts can be displayed at the end.
//
// Two consecutive runs always use different ports, PIDs, PGIDs, and temp directories.
//
// Usage:
//   node e2e/pw.js [playwright-test-args]
//   npm test                  # same, via package.json script

import { createServer } from 'net';
import { spawnSync } from 'child_process';
import { mkdtempSync, readFileSync, existsSync, rmSync } from 'fs';
import { tmpdir } from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

// Reserve two distinct ports with both sockets open simultaneously.
// This guarantees they are distinct (OS cannot assign the same port twice while
// both sockets are open) and minimises the TOCTOU window between close and bind.
function reserveTwoPorts() {
  return new Promise((resolve, reject) => {
    const srv1 = createServer();
    srv1.listen(0, '127.0.0.1', () => {
      const port1 = srv1.address().port;
      const srv2 = createServer();
      srv2.listen(0, '127.0.0.1', () => {
        const port2 = srv2.address().port;
        // Both sockets are bound simultaneously — port1 ≠ port2 guaranteed.
        srv1.close((e1) => {
          srv2.close((e2) => {
            if (e1 || e2) reject(e1 || e2);
            else resolve([port1, port2]);
          });
        });
      });
      srv2.on('error', (e) => { srv1.close(() => reject(e)); });
    });
    srv1.on('error', reject);
  });
}

async function main() {
  // ── Reserve two distinct ports simultaneously ─────────────────────────────
  const [skPort, goPort] = await reserveTwoPorts();
  console.log(`[pw] SK=${skPort} Go=${goPort}`);

  // ── Create unique temp run directory ─────────────────────────────────────
  const runDir = mkdtempSync(path.join(tmpdir(), 'v2test-'));
  console.log(`[pw] run dir: ${runDir}`);

  // ── Report path (written by Playwright) ──────────────────────────────────
  const reportPath = path.join(runDir, 'pw-report.json');

  // ── Run Playwright with pre-set env ──────────────────────────────────────
  const frontendDir = path.resolve(fileURLToPath(import.meta.url), '../..');
  const env = {
    ...process.env,
    PW_SK_PORT: String(skPort),
    PW_GO_PORT: String(goPort),
    PW_RUN_DIR: runDir,
    // JSON reporter written to runDir for post-run summary.
    PLAYWRIGHT_JSON_OUTPUT_NAME: reportPath,
  };

  const result = spawnSync(
    'npx',
    ['playwright', 'test', '--reporter=list,json', ...process.argv.slice(2)],
    { env, stdio: 'inherit', cwd: frontendDir }
  );

  // ── Print critical / smoke counts from JSON report ────────────────────────
  try {
    if (existsSync(reportPath)) {
      const report = JSON.parse(readFileSync(reportPath, 'utf-8'));
      let criticalCount = 0;
      let smokeCount = 0;
      for (const suite of (report.suites || [])) {
        const file = suite.file || suite.title || '';
        const tests = (suite.specs || []).length;
        if (file.includes('critical')) criticalCount += tests;
        else if (file.includes('smoke')) smokeCount += tests;
        // mobile spec counts as critical
        else if (file.includes('mobile')) criticalCount += tests;
      }
      console.log(`\n[pw] ── Run summary ──────────────────────────────`);
      console.log(`[pw]   run dir:       ${runDir}`);
      console.log(`[pw]   SK port:       ${skPort}`);
      console.log(`[pw]   Go port:       ${goPort}`);
      console.log(`[pw]   critical count: ${criticalCount}`);
      console.log(`[pw]   smoke count:    ${smokeCount}`);
      console.log(`[pw]   exit status:    ${result.status ?? 1}`);
    }
  } catch (e) {
    console.error('[pw] report parse error:', e.message);
  }

  process.exit(result.status ?? 1);
}

main().catch((e) => { console.error('[pw] fatal:', e); process.exit(1); });
