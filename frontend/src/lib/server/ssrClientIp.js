// Decides what X-Forwarded-For value (if any) an SSR-originated fetch to the
// backend should carry, so the backend's rate limiter attributes it to the
// real, distinct visitor instead of collapsing every SSR request into one
// shared bucket.
//
// This mirrors internal/v2/clientip.go's RealClientIP trust model exactly,
// because it exists to feed that exact function correctly:
//
//  1. If the immediate peer of the request reaching THIS Node process
//     (getClientAddress()) is missing or does not parse as a valid IP, it is
//     NOT confirmed loopback — proxy headers are never read in this case,
//     and no header is forwarded. An unparseable/absent immediate address
//     must never be treated as "trusted proxy hop" by default.
//  2. If the immediate peer IS a valid, confirmed-non-loopback IP, it is
//     authoritative on its own — no proxy header is trusted in that case,
//     exactly as clientip.go refuses proxy headers from a non-loopback
//     RemoteAddr.
//  3. Only if the immediate peer is a valid IP AND confirmed loopback (the
//     production topology: Caddy → naroom-web over 127.0.0.1, or
//     naroom-web's own SSR fetch → backend over 127.0.0.1) is the ORIGINAL
//     incoming request's own X-Forwarded-For (last valid entry) trusted,
//     then X-Real-IP as fallback — because Caddy is documented
//     (docs/v2/CADDY_REAL_IP_PATCH.md) to set these on its connection to
//     naroom-web with the real visitor IP, the same way it does for its
//     connection to the Go backend. This is NOT something getClientAddress()
//     alone can recover if naroom-web.service does not set
//     ADDRESS_HEADER=X-Forwarded-For for @sveltejs/adapter-node — reading
//     the header directly here does not depend on that env var at all.
//  4. A malformed, empty, or loopback candidate at any stage is discarded,
//     never forwarded. If no valid non-loopback visitor IP can be
//     determined, no header is sent at all — the backend then falls back to
//     its own RemoteAddr for that request. That fallback is a real,
//     acknowledged limitation (it CAN still collapse into one bucket if
//     Caddy is not correctly forwarding real-IP headers to naroom-web) — this
//     module does not claim to eliminate that case, only to correctly use
//     whatever real-IP signal is actually available.
import net from 'node:net';

// Strips an optional ":port" suffix. Handles bracketed IPv6 ("[::1]:1234")
// and IPv4-with-port ("1.2.3.4:1234"); leaves bare IPv6 ("::1") untouched,
// since blindly splitting on ":" would mangle it.
function hostOnly(addr) {
	if (!addr) return addr;
	const bracketed = addr.match(/^\[(.+)\](?::\d+)?$/);
	if (bracketed) return bracketed[1];
	const parts = addr.split(':');
	if (parts.length === 2 && net.isIP(parts[0])) return parts[0];
	return addr;
}

// Returns a validated, port-stripped IP string, or null if candidate is
// empty, malformed, or otherwise not a real IP address.
function normalizeIP(candidate) {
	const host = hostOnly((candidate || '').trim());
	if (!host || !net.isIP(host)) return null;
	return host;
}

function isLoopback(ip) {
	if (!ip) return true;
	const v = ip.replace(/^::ffff:/, '');
	return v === '::1' || v === '127.0.0.1' || /^127\./.test(v);
}

// Mirrors internal/v2/clientip.go's lastValidForwardedIP: scans a
// comma-separated X-Forwarded-For chain from the end (the entry closest to
// our own trusted hop) and returns the first one that parses as a valid IP.
function lastValidForwardedIP(header) {
	if (!header) return null;
	const parts = header.split(',');
	for (let i = parts.length - 1; i >= 0; i--) {
		const ip = normalizeIP(parts[i]);
		if (ip) return ip;
	}
	return null;
}

// immediateAddress: getClientAddress() — the peer connecting to this Node
// process. xForwardedFor / xRealIp: the SAME headers from the ORIGINAL
// incoming request (request.headers.get(...)), not anything this module
// invents. Returns {} or a single-entry X-Forwarded-For headers object.
export function buildForwardedForHeaders(immediateAddress, xForwardedFor, xRealIp) {
	const immediate = normalizeIP(immediateAddress);

	// Missing or unparseable immediate address: this can NEVER be confirmed
	// loopback, so the trusted-proxy-hop path below must not run — proxy
	// headers are never read, and no header is forwarded. Falling through
	// here would let a spoofed X-Forwarded-For be trusted whenever
	// getClientAddress() is merely absent/malformed, not actually loopback.
	if (!immediate) {
		return {};
	}

	// Immediate peer confirmed non-loopback: it is authoritative. Proxy
	// headers are ignored — an untrusted intermediary could forge them, the
	// same reasoning clientip.go applies to a non-loopback RemoteAddr.
	if (!isLoopback(immediate)) {
		return { 'X-Forwarded-For': immediate };
	}

	// Immediate peer is a valid IP AND confirmed loopback: only now is the
	// trusted-proxy-hop path taken.
	const fromXff = lastValidForwardedIP(xForwardedFor);
	if (fromXff && !isLoopback(fromXff)) {
		return { 'X-Forwarded-For': fromXff };
	}
	const fromRealIp = normalizeIP(xRealIp);
	if (fromRealIp && !isLoopback(fromRealIp)) {
		return { 'X-Forwarded-For': fromRealIp };
	}

	// No valid, non-loopback visitor IP available from any source: send no
	// header at all, rather than forward something malformed or misleading.
	return {};
}
