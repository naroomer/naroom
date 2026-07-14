#!/usr/bin/env node
// run-all.js — orchestrates all E2E tests sequentially
import { run as test001 } from './tests/001_happy_path.js';
import { run as test002 } from './tests/002_stale_room_guard.js';
import { run as test003 } from './tests/003_role_separation_review.js';
import { run as test004 } from './tests/004_remote_close_state.js';
import { run as test005 } from './tests/005_large_image_payload.js';
import { run as test006 } from './tests/006_state_bleed.js';
import { run as test007 } from './tests/007_rate_limiting.js';
import { run as test008 } from './tests/008_wallet_challenge.js';
import { run as test009 } from './tests/009_session_lifecycle.js';
import { run as test010 } from './tests/010_ws_auth.js';
import { run as test011 } from './tests/011_peer_left_expiry.js';
import { run as test013 } from './tests/013_invoice_scoping.js';
import { run as test014 } from './tests/014_reputation.js';

const tests = [test001, test002, test003, test004, test005, test006,
               test007, test008, test009, test010, test011, test013, test014];
const results = [];

console.log('\n╔══════════════════════════════════╗');
console.log('║   NA Room E2E Test Suite         ║');
console.log('╚══════════════════════════════════╝');

for (const test of tests) {
  try {
    const passed = await test();
    results.push(passed);
  } catch(e) {
    console.error('  FATAL:', e.message);
    results.push(false);
  }
}

const allPassed = results.every(Boolean);
console.log('\n══════════════════════════════════');
console.log(`  OVERALL: ${results.filter(Boolean).length}/${results.length} test suites passed`);
if (!allPassed) {
  console.error('  RESULT: FAILED ✗');
  process.exit(1);
} else {
  console.log('  RESULT: ALL PASSED ✓');
}
