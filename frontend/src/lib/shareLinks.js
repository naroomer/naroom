// Builds outbound share URLs for the public city board — the only place in
// the app that constructs a UTM-tagged link. Deliberately pure and
// side-effect-free (no navigator.*, no DOM) so it is directly unit-testable.
//
// Safety: the city slug is checked against the SAME registry array the board
// page already loads from the backend (/api/v2/board/cities, itself sourced
// from internal/v2's EnabledCities() allowlist) — an unknown/unlisted slug
// is rejected outright, returning null rather than building a URL for it.
// The URL is assembled via the URL/URLSearchParams constructors, never by
// concatenating raw strings, and carries only the public city slug plus a
// fixed, hardcoded set of UTM values — nothing else is ever accepted into
// it (no wallet, token, contact, or listing data ever passes through here).
import { SITE_ORIGIN } from './seoConfig.js';

export function isKnownCity(citySlug, cities) {
	return Array.isArray(cities) && cities.some((c) => c && c.id === citySlug);
}

// utmSource: the fixed campaign source for this share surface ('copy_link'
// or 'native_share' — the only two callers in the app). Returns null when
// citySlug is not a known, enabled city.
export function buildBoardShareUrl(citySlug, cities, utmSource) {
	if (!isKnownCity(citySlug, cities)) return null;

	const url = new URL(`${SITE_ORIGIN}/v2/board/${encodeURIComponent(citySlug)}`);
	const params = new URLSearchParams();
	params.set('utm_source', utmSource);
	params.set('utm_medium', 'referral');
	params.set('utm_campaign', 'board_share');
	params.set('utm_content', citySlug);
	url.search = params.toString();

	return url.toString();
}
