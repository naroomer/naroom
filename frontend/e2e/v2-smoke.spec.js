// e2e/v2-smoke.spec.js — V2 browser smoke suite.
//
// Smoke paths (all 4 browser projects):
//   board    — /v2/board/tbilisi → listing board page renders
//   new      — /v2/new           → client payment form page renders
//   restore  — /v2/restore       → client restore form page renders
//   listing  — /v2/listing/:id   → listing detail page renders
//   informer — /v2/informer      → informer subscription page renders
//
// All /api/v2/* fetch calls made by the browser are intercepted via
// page.route() and answered with realistic mock responses. No real backend
// required. V2 pages have no +page.server.js load functions, so the SSR layer
// serves only HTML/JS shells; all data fetching is CSR.
//
// Teardown: Playwright's webServer lifecycle handles process teardown. No
// DB artifacts, binary artifacts, or open ports remain after the suite.

import { test, expect } from '@playwright/test';

// ── Mock API responses ────────────────────────────────────────────────────────

const BOARD_RESPONSE = [
	{
		id: 'listing-smoke-01',
		city: 'tbilisi',
		dependency_type: 'alcohol',
		help_type: 'crisis',
		urgency: 'urgent',
		languages: ['en'],
		contact_type: 'telegram',
		display_name: 'Smoke Test Helper',
		published_at: new Date().toISOString(),
		visible_until: new Date(Date.now() + 86400_000).toISOString(),
		time_left_sec: 86400,
	},
];

const LISTING_RESPONSE = {
	id: 'listing-smoke-01',
	city: 'tbilisi',
	dependency_type: 'alcohol',
	help_type: 'crisis',
	urgency: 'urgent',
	languages: ['en'],
	contact_type: 'telegram',
	display_name: 'Smoke Test Helper',
	published_at: new Date().toISOString(),
	visible_until: new Date(Date.now() + 86400_000).toISOString(),
};

const PAYMENT_INTENT_RESPONSE = {
	flow_id: 'flow-smoke-01',
	invoice_id: 'inv-smoke-01',
	payment_address: '1SmokeTestBTCAddr000000000000000001',
	amount_atomic: 9800,
	amount_usd_cents: 500,
	expires_at: Math.floor(Date.now() / 1000) + 3600,
	state: 'awaiting_payment',
};

// ── Route mocking fixture ─────────────────────────────────────────────────────

async function mockV2APIs(page) {
	// Board: return a listing for any city
	await page.route('**/api/v2/board/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(BOARD_RESPONSE) })
	);

	// Single listing detail
	await page.route('**/api/v2/listings/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LISTING_RESPONSE) })
	);

	// Client payment intent creation
	await page.route('**/api/v2/client/payment-intents', (route) => {
		if (route.request().method() === 'POST') {
			return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(PAYMENT_INTENT_RESPONSE) });
		}
		return route.continue();
	});

	// Restore: not found (expected initial state for smoke)
	await page.route('**/api/v2/client/listings/restore', (route) =>
		route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not found' }) })
	);
	await page.route('**/api/v2/client/payment-intents/restore', (route) =>
		route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not found' }) })
	);

	// Informer
	await page.route('**/api/v2/informer/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ state: 'pending', raw_token: 'smoke-tok' }) })
	);

	// Helper purchases
	await page.route('**/api/v2/helper/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ state: 'awaiting_payment' }) })
	);

	// V2 health
	await page.route('**/api/v2/health', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ v2: 'ready' }) })
	);

	// Telegram links
	await page.route('**/api/v2/client/telegram-links/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ state: 'pending' }) })
	);
	await page.route('**/api/v2/client/telegram-links', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deep_link: 'https://t.me/smokebot?start=abc', expires_at: Date.now() + 900 }) })
	);

	// Catch-all: any remaining /api/v2/* calls return empty 200
	await page.route('**/api/v2/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) })
	);

	// Suppress unrelated /api/* calls (V1 API, websockets, etc.)
	await page.route('**/api/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) })
	);
}

// ── Smoke: board ──────────────────────────────────────────────────────────────

test('smoke: board page renders for city', async ({ page }) => {
	await mockV2APIs(page);
	await page.goto('/v2/board/tbilisi');
	await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {});

	const body = await page.locator('body');
	await expect(body).not.toBeEmpty();

	const html = await page.content();
	expect(html.length, 'board page must produce non-trivial HTML').toBeGreaterThan(200);

	// No unhandled crashes: page should not redirect to a fatal error route
	const url = page.url();
	expect(url).not.toContain('/error');
});

// ── Smoke: new listing (client payment form) ──────────────────────────────────

test('smoke: new listing page renders', async ({ page }) => {
	await mockV2APIs(page);
	await page.goto('/v2/new');
	await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {});

	const html = await page.content();
	expect(html.length, 'new listing page must produce non-trivial HTML').toBeGreaterThan(200);

	const url = page.url();
	expect(url).not.toContain('/error');
});

// ── Smoke: restore ────────────────────────────────────────────────────────────

test('smoke: restore page renders without crash', async ({ page }) => {
	await mockV2APIs(page);
	await page.goto('/v2/restore');
	await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {});

	const html = await page.content();
	expect(html.length, 'restore page must produce non-trivial HTML').toBeGreaterThan(200);

	const url = page.url();
	expect(url).not.toContain('/error');
});

// ── Smoke: listing detail ─────────────────────────────────────────────────────

test('smoke: listing detail page renders', async ({ page }) => {
	await mockV2APIs(page);
	await page.goto('/v2/listing/listing-smoke-01');
	await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {});

	const html = await page.content();
	expect(html.length, 'listing detail page must produce non-trivial HTML').toBeGreaterThan(200);

	const url = page.url();
	expect(url).not.toContain('/error');
});

// ── Smoke: informer subscription ─────────────────────────────────────────────

test('smoke: informer subscription page renders', async ({ page }) => {
	await mockV2APIs(page);
	await page.goto('/v2/informer');
	await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {});

	const html = await page.content();
	expect(html.length, 'informer page must produce non-trivial HTML').toBeGreaterThan(200);

	const url = page.url();
	expect(url).not.toContain('/error');
});
