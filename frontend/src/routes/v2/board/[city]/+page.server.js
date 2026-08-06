import { isLaunchListing } from '$lib/v2LaunchPolicy.js';
import { buildForwardedForHeaders } from '$lib/server/ssrClientIp.js';

// Direct backend origin for server-side (SSR) fetches, which bypass Vite's
// dev proxy / Caddy entirely (that proxy only intercepts browser-originated
// requests reaching the frontend's own HTTP server). Reuses the SAME
// BACKEND_URL_V2 / BACKEND_URL env vars vite.config.js already defines for
// exactly this purpose (dynamic backend port in e2e tests; unset in
// production, where both default to the colocated backend on :8080 — the
// same default Caddy forwards /api/v2/* to). This is additive and does not
// change Caddy, the backend, or the existing browser-side /api/v2/* path.
const BACKEND_INTERNAL_URL =
	process.env.BACKEND_URL_V2 || process.env.BACKEND_URL || 'http://127.0.0.1:8080';

// Public, launch-safe fields only — matches what the board UI actually
// renders. The backend's public board endpoint already excludes contact
// data, capability tokens, payment info and internal IDs by contract (see
// internal/v2/listing_http.go: publicListingJSON), but this SSR path
// re-whitelists explicitly rather than passing the raw response through, so
// no future backend field addition can leak into server-rendered HTML
// unnoticed.
function toSafeListing(l) {
	return {
		id: l.id,
		display_name: l.display_name,
		city: l.city,
		dependency_type: l.dependency_type,
		help_type: l.help_type,
		urgency: l.urgency,
		languages: l.languages,
		client_reputation: l.client_reputation
			? {
					member_since: l.client_reputation.member_since,
					positive_count: l.client_reputation.positive_count,
					negative_count: l.client_reputation.negative_count,
				}
			: null,
	};
}

export async function load({ params, fetch, getClientAddress, request }) {
	const city = params.city;

	let cities = [];
	let listings = [];

	// See $lib/server/ssrClientIp.js for the full trust-boundary rationale.
	// getClientAddress() alone is not enough: in production it reflects
	// Caddy's own (loopback) connection to naroom-web unless
	// ADDRESS_HEADER=X-Forwarded-For is set for adapter-node. Caddy is
	// documented to forward the real visitor IP via X-Forwarded-For /
	// X-Real-IP regardless of that env var, so this reads those headers from
	// the ORIGINAL incoming request too and lets the helper apply the same
	// loopback trust boundary internal/v2/clientip.go uses.
	let clientAddress = '';
	try {
		clientAddress = getClientAddress();
	} catch {}
	const forwardHeaders = buildForwardedForHeaders(
		clientAddress,
		request.headers.get('x-forwarded-for'),
		request.headers.get('x-real-ip'),
	);

	try {
		const [citiesRes, boardRes] = await Promise.all([
			fetch(`${BACKEND_INTERNAL_URL}/v2/board/cities`, { headers: forwardHeaders }),
			fetch(`${BACKEND_INTERNAL_URL}/v2/board/${encodeURIComponent(city)}`, { headers: forwardHeaders }),
		]);
		if (citiesRes.ok) {
			const data = await citiesRes.json();
			if (Array.isArray(data)) cities = data;
		}
		if (boardRes.ok) {
			const data = await boardRes.json();
			if (Array.isArray(data)) listings = data.filter(isLaunchListing).map(toSafeListing);
		}
	} catch {
		// Backend unreachable during SSR: fall back to an empty initial state.
		// The existing client-side onMount fetch (unchanged) retries normally
		// after hydration, exactly as it does today.
	}

	return { cities, listings };
}
