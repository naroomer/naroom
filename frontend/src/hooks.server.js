// src/hooks.server.js — proxy /api/v2/* requests to the Go test server when
// V2_API_URL env var is set. Used only during Playwright critical-path E2E tests.

import { SEO_MANAGED_ROUTE_IDS, SUPPORTED_SEO_LANGS } from '$lib/seoConfig.js';

// Private/operational V2 HTML pages: never indexed. Kept in sync with the
// <meta name="robots"> tag each of these pages sets itself — this header is
// a second, server-level enforcement of the same decision (per-request,
// cannot be skipped by a crawler that ignores HTML meta tags).
//
// Deliberately does NOT include /api/ — in production /api/* is served
// directly by the Go backend via Caddy, never through this Node process, so
// a header set here would never reach that response. robots.txt's
// `Disallow: /api/` is the actual and sufficient mechanism for the API
// surface; this header only ever matters for the HTML pages below.
const PRIVATE_PATH_PATTERNS = [
    /^\/v2\/new(\/|$)/,
    /^\/v2\/restore(\/|$)/,
    /^\/v2\/informer(\/|$)/,
    /^\/v2\/listing\//,
    /^\/v2\/helper\//,
];

function isPrivatePath(pathname) {
    return PRIVATE_PATH_PATTERNS.some((re) => re.test(pathname));
}

// SvelteKit route IDs for the two SEO-managed pages, matched against
// event.route.id (the [city]-bracket pattern SvelteKit resolves per request,
// available inside handle() the same way it is in +page.svelte via
// $app/state). Used to pick the server-rendered <html lang> below.
function isSeoManagedRouteId(routeId) {
    return SEO_MANAGED_ROUTE_IDS.includes(routeId);
}

export async function handle({ event, resolve }) {
    const v2ApiUrl = process.env.V2_API_URL;
    if (v2ApiUrl && event.url.pathname.startsWith('/api/v2')) {
        const targetUrl = v2ApiUrl + event.url.pathname + event.url.search;
        try {
            const res = await fetch(targetUrl, {
                method: event.request.method,
                headers: Object.fromEntries(
                    [...event.request.headers].filter(([k]) => k !== 'host')
                ),
                body: ['GET', 'HEAD'].includes(event.request.method)
                    ? undefined
                    : await event.request.text(),
            });
            return new Response(await res.text(), {
                status: res.status,
                headers: { 'content-type': res.headers.get('content-type') ?? 'application/json' },
            });
        } catch (e) {
            return new Response(JSON.stringify({ error: 'proxy error' }), { status: 502 });
        }
    }

    // <html lang> for the two SEO-managed pages must match their own ?lang=
    // (fallback 'en'), without any visual change — app.html emits
    // %sveltekit.html.attributes% instead of a hardcoded lang="en" so this
    // can replace it per request. Every other route keeps the same safe
    // 'en' fallback that was previously hardcoded directly in app.html.
    let htmlLang = 'en';
    if (isSeoManagedRouteId(event.route.id)) {
        const param = event.url.searchParams.get('lang');
        if (SUPPORTED_SEO_LANGS.includes(param)) htmlLang = param;
    }

    const response = await resolve(event, {
        transformPageChunk: ({ html }) => html.replace('%sveltekit.html.attributes%', `lang="${htmlLang}"`),
    });
    if (isPrivatePath(event.url.pathname)) {
        response.headers.set('X-Robots-Tag', 'noindex, nofollow, noarchive');
    }
    return response;
}
