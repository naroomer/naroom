/**
 * Analytics helpers — GoatCounter, public pages only.
 *
 * Allowed routes (whitelist):
 *   /              — landing page
 *   /how-it-works  — public info page
 *   /board/*       — public city boards
 *
 * Excluded (everything else, including /new, /listing/*, /chat/*, /helper):
 *   These pages contain wallet, session, chat, listing-private, or payment state.
 *   No analytics script is loaded or invoked on these routes.
 */

const ANALYTICS_EXACT = ['/', '/how-it-works'];
const ANALYTICS_PREFIX = ['/board/'];

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
