package v2

import (
	"net"
	"net/http"
	"strings"
)

// RealClientIP returns the best-effort real client IP for a request, for use
// as a rate-limit bucket key. This is the single shared helper used by every
// V2 endpoint's rate-limit key function.
//
// Trust model:
//
//  1. The immediate TCP peer (r.RemoteAddr) is authoritative by default.
//  2. A proxy header (X-Forwarded-For, then X-Real-IP) is trusted ONLY when
//     the immediate TCP peer is loopback (127.0.0.0/8 or ::1) — i.e. a local
//     reverse proxy (Caddy) running on the same host. Production Caddy must
//     be configured to overwrite this header with the real client address
//     before forwarding (see docs/v2/CADDY_REAL_IP_PATCH.md); this function
//     does not and cannot verify that on its own, but a public client can
//     never reach this backend directly without going through that loopback
//     hop in the deployed topology.
//  3. A public client connecting directly (RemoteAddr not loopback) cannot
//     spoof its rate-limit identity via these headers — they are ignored
//     whenever the immediate peer is not loopback.
//  4. If the proxy header is absent or malformed even though the peer is a
//     trusted loopback proxy, the function falls back safely to RemoteAddr
//     rather than failing or panicking.
//
// The returned string is a normalized IP (via net.ParseIP().String()) with
// no port. Callers are responsible for hashing it (HMAC) before using it as
// a map key, exactly as before this helper existed.
func RealClientIP(r *http.Request) string {
	remoteHost := hostOnly(r.RemoteAddr)

	if !isTrustedLoopbackPeer(remoteHost) {
		// Direct (or untrusted-intermediary) request: RemoteAddr is authoritative.
		return normalizeOrRaw(remoteHost, r.RemoteAddr)
	}

	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if ip := lastValidForwardedIP(fwd); ip != "" {
			return ip
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		if ip := net.ParseIP(hostOnly(real)); ip != nil {
			return ip.String()
		}
	}

	// Trusted proxy hop but no usable header — safe fallback to RemoteAddr.
	return normalizeOrRaw(remoteHost, r.RemoteAddr)
}

// isTrustedLoopbackPeer reports whether host (already stripped of any port)
// is a loopback address, meaning the request arrived via a local reverse
// proxy rather than directly from the public internet.
func isTrustedLoopbackPeer(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// hostOnly strips an optional ":port" suffix from addr. If addr has no port
// (or is otherwise not a valid "host:port" pair), it is returned unchanged.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// normalizeOrRaw parses host as an IP and returns its canonical string form,
// collapsing IPv4/IPv6 format variants. If host does not parse as an IP,
// the original raw address is returned unchanged (safe fallback — the value
// is only ever used as an opaque bucket key, never dereferenced as an IP).
func normalizeOrRaw(host, raw string) string {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return raw
}

// lastValidForwardedIP parses a comma-separated X-Forwarded-For header and
// returns the last entry that parses as a valid IP address. Under a
// single-trusted-hop model, the entry closest to our own proxy (the last
// one) is the one our proxy itself is responsible for and is therefore the
// most trustworthy — any earlier entries could have been supplied by the
// original client and must not be trusted blindly. Returns "" if no entry
// parses, signalling the caller to fall back to RemoteAddr.
func lastValidForwardedIP(header string) string {
	parts := strings.Split(header, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		if candidate == "" {
			continue
		}
		candidate = hostOnly(candidate)
		if ip := net.ParseIP(candidate); ip != nil {
			return ip.String()
		}
	}
	return ""
}
