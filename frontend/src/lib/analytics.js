/**
 * Analytics helpers — GoatCounter, public pages only.
 *
 * Allowed routes (whitelist):
 *   /                 — landing page (legacy V1)
 *   /how-it-works     — public info page (legacy V1)
 *   /board/*          — public city boards (legacy V1)
 *   /v2/how-it-works  — public info page (V2, current production route)
 *   /v2/board/*       — public city boards (V2, current production route)
 *
 * Excluded (everything else, including /new, /listing/*, /chat/*, /helper, /resume,
 * and their V2 equivalents /v2/new, /v2/restore, /v2/informer, /v2/listing/*, /v2/helper/*):
 *   These pages contain wallet, session, chat, listing-private, or payment state.
 *   No analytics script is loaded or invoked on these routes.
 */

const ANALYTICS_EXACT = ['/', '/how-it-works', '/v2/how-it-works'];
const ANALYTICS_PREFIX = ['/board/', '/v2/board/'];

/**
 * Returns true only for routes that are safe to track.
 * Uses a whitelist — anything not explicitly listed is excluded.
 * @param {string} pathname
 * @returns {boolean}
 */
export function isAnalyticsRoute(pathname) {
	if (ANALYTICS_EXACT.includes(pathname)) return true;
	return ANALYTICS_PREFIX.some((p) => pathname.startsWith(p));
}
