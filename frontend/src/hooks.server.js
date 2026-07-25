// src/hooks.server.js — proxy /api/v2/* requests to the Go test server when
// V2_API_URL env var is set. Used only during Playwright critical-path E2E tests.
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
    return resolve(event);
}
