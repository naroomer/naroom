// Shared constants for the two SEO-indexable V2 routes (how-it-works, board).
// Single source of truth so the language-URL model, fixed canonical origin,
// and OG locale mapping can never drift between +layout.svelte,
// hooks.server.js, and the two page components.

// Canonical/hreflang/OG/JSON-LD must always use the production origin,
// regardless of the actual request Host header (dev, preview, or any other
// environment must never leak into published metadata).
export const SITE_ORIGIN = 'https://naroom.net';

export const SUPPORTED_SEO_LANGS = ['en', 'ru', 'es', 'ka'];

export const OG_LOCALE = { en: 'en_US', ru: 'ru_RU', es: 'es_ES', ka: 'ka_GE' };

// SvelteKit route IDs (the [city]-bracket pattern, not a resolved path) for
// the two routes whose language is authoritatively the URL's ?lang= — not
// the shared $lang store/localStorage — and whose <SeoHead> is the only
// source of <title>/description/OG/Twitter metadata (see +layout.svelte).
export const SEO_MANAGED_ROUTE_IDS = ['/v2/how-it-works', '/v2/board/[city]'];

// Resolves the effective language for a request/navigation URL: valid
// ?lang= wins, otherwise 'en'. Used identically during SSR and after
// hydration (both read the same page.url), so canonical/hreflang and the
// rendered text never flip to the browser's language post-hydration — the
// URL is authoritative on these two pages, by design.
export function langFromUrl(url) {
	const param = url.searchParams.get('lang');
	return SUPPORTED_SEO_LANGS.includes(param) ? param : 'en';
}

// Builds the query string suffix for a given language: '' for 'en' (bare
// canonical URL), '?lang=xx' otherwise.
export function langQuery(lang) {
	return lang === 'en' ? '' : `?lang=${lang}`;
}
