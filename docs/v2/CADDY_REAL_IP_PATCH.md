# Caddy real-IP note for V2 rate limiting (corrected — no Caddy change required)

## Status

**Correction (this revision):** an earlier version of this document incorrectly
described Caddy's default `reverse_proxy` behavior for `X-Forwarded-For` and,
as a result, incorrectly proposed a mandatory Caddy config change. That
description was wrong. This revision corrects it after checking the official
Caddy documentation and the actual production Caddyfile (read-only). No
production file has been modified while writing either version of this
document.

## What Caddy actually does by default (verified)

Per the official documentation
(https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#headers):

> For the `X-Forwarded-*` headers, by default, the proxy will ignore their
> values from incoming requests, to prevent spoofing.

In other words, **by default Caddy's `reverse_proxy` does not trust or forward
a client-supplied `X-Forwarded-For` value at all** — it discards whatever the
client sent and sets the header itself, from the address of the immediate TCP
connection it received. This is the opposite of what the previous revision of
this document claimed (that Caddy appends to/preserves an inbound value by
default).

This default can be changed with the `trusted_proxies` global option: only
when a request's immediate peer address falls inside a `trusted_proxies` CIDR
range does Caddy accept and forward an inbound `X-Forwarded-For` value instead
of overwriting it. `trusted_proxies` exists for topologies where something
else (a CDN, an upstream load balancer) sits in front of Caddy itself.

## Current production topology (read-only checks performed)

- `ssh root@naroom.net 'grep -n trusted_proxies /etc/caddy/Caddyfile'` →
  **no matches**. `trusted_proxies` is not configured anywhere in the current
  production Caddyfile.
- Caddy is the public edge here — it terminates TLS directly for
  `naroom.net` and the `:8090` Tor hidden service, and reverse-proxies
  `/api/*` to `localhost:8080` (the Go backend) on the same host. There is no
  additional CDN/load balancer in front of Caddy in this topology.
- `caddy version` on the VPS: `v2.11.4`.

Given no `trusted_proxies` is configured and Caddy is the actual public edge,
Caddy's **default, unmodified** behavior already does exactly what
`internal/v2/clientip.go` (`RealClientIP`) needs: it ignores anything the
public client puts in `X-Forwarded-For` and sets that header itself from the
real immediate client address before forwarding to `localhost:8080`. The Go
backend then trusts that header only because the peer delivering it
(`RemoteAddr`) is loopback (Caddy itself) — and by the time it reaches Caddy,
the header already contains only Caddy's own honest value, never anything the
original client supplied.

**Conclusion: no Caddy configuration change is required for the
`RealClientIP` backend fix to be safe and effective in the current topology.**
The backend fix alone is sufficient.

## Optional defense-in-depth (not required, NOT applied)

An explicit `header_up X-Forwarded-For {remote_host}` could be added to make
the intent self-documenting in the Caddyfile itself, purely for readability —
it does not change behavior, since it duplicates what Caddy already does by
default. This was verified directly, not assumed:

```bash
# Read-only syntax check against a throwaway temp file — never touched the
# real /etc/caddy/Caddyfile, never reloaded anything:
caddy adapt --config /tmp/caddy_syntax_check_readonly.Caddyfile
```

Result: valid config (exit code 0), but Caddy itself emits:

```
{"level":"warn",...,"msg":"Unnecessary header_up X-Forwarded-For: the reverse proxy's default behavior is to pass headers to the upstream"}
```

Caddy's own tooling confirms this directive is redundant given the current
topology. It is documented here only as an optional, non-functional
readability improvement — **it must not be applied**, since it adds nothing
and the previous revision's justification for it (that it was required to
prevent spoofing) was based on the incorrect default-behavior claim corrected
above.

## Current production Caddyfile (read-only, as of this writing)

```caddyfile
naroom.net {
    handle /api/* {
        uri strip_prefix /api
        reverse_proxy localhost:8080
    }
    handle /ws/* {
        uri strip_prefix /ws
        reverse_proxy localhost:8080
    }
    handle /_app/immutable/* {
        header Cache-Control "public, max-age=31536000, immutable"
        reverse_proxy localhost:3000
    }
    handle {
        header Cache-Control "no-cache, no-store, must-revalidate"
        reverse_proxy localhost:3000
    }
}

# Tor Hidden Service — HTTP без TLS
:8090 {
    ...
    handle /api/* {
        uri strip_prefix /api
        reverse_proxy localhost:8080
    }
    ...
}
```

No changes are proposed to this file. It is included here only as the
as-checked reference for the read-only findings above.

## What actually ships

Only the backend change (`internal/v2/clientip.go` and the five call sites it
replaced) is needed. There is no corresponding Caddy deploy step for this fix
— Caddy's current, already-deployed, default configuration is sufficient.
