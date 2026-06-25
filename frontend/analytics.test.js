#!/usr/bin/env node
/**
 * analytics.test.js — unit tests for GoatCounter route whitelist.
 *
 * Tests the isAnalyticsRoute() function from src/lib/analytics.js.
 * Because this is a plain ES module with no SvelteKit imports, it can run
 * directly with: node analytics.test.js
 *
 * Mirrors the logic in src/lib/analytics.js — must be kept in sync.
 */

// ── Inline the same logic (no SvelteKit imports needed in test) ───────────────

const ANALYTICS_EXACT = ['/', '/how-it-works'];
const ANALYTICS_PREFIX = ['/board/'];

function isAnalyticsRoute(pathname) {
	if (ANALYTICS_EXACT.includes(pathname)) return true;
	return ANALYTICS_PREFIX.some((p) => pathname.startsWith(p));
}

// ── Test runner ───────────────────────────────────────────────────────────────

let passed = 0;
let failed = 0;

function check(label, actual, expected) {
	if (actual === expected) {
		console.log(`  ✓ ${label}`);
		passed++;
	} else {
		console.error(`  ✗ ${label}  →  expected ${expected}, got ${actual}`);
		failed++;
	}
}

// ── Allowed routes (must return true) ────────────────────────────────────────

console.log('\nAllowed routes (analytics ON):');
check('/', isAnalyticsRoute('/'), true);
check('/how-it-works', isAnalyticsRoute('/how-it-works'), true);
check('/board/london', isAnalyticsRoute('/board/london'), true);
check('/board/new-york', isAnalyticsRoute('/board/new-york'), true);
check('/board/tbilisi', isAnalyticsRoute('/board/tbilisi'), true);

// ── Excluded routes (must return false) ───────────────────────────────────────

console.log('\nExcluded routes (analytics OFF):');
check('/new', isAnalyticsRoute('/new'), false);
check('/helper', isAnalyticsRoute('/helper'), false);
check('/listing/abc123', isAnalyticsRoute('/listing/abc123'), false);
check('/chat/room456', isAnalyticsRoute('/chat/room456'), false);
check('/board', isAnalyticsRoute('/board'), false);          // prefix only, not exact
check('/board', isAnalyticsRoute('/boardgame'), false);      // no false prefix match
check('/how-it-worksextra', isAnalyticsRoute('/how-it-worksextra'), false);
check('', isAnalyticsRoute(''), false);

// ── Summary ───────────────────────────────────────────────────────────────────

console.log(`\n  ${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
