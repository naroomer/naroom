# V2 Deploy Runbook — naroom.net (VPS/systemd/Caddy)

> **READ-ONLY RUNBOOK — DO NOT EXECUTE.** This document describes steps for
> a human operator. No commands here are run automatically.
>
> Production infrastructure: VPS naroom.net, binary at `/opt/naroom/naroom`,
> frontend at `/opt/naroom-web/`, env at `/opt/naroom/.env`,
> systemd units `naroom.service` + `naroom-web.service`,
> Caddy as TLS reverse proxy, backups at `/opt/backups/naroom/`.
>
> **DB_PATH is not guessed here.** The operator must determine the actual SQLite
> path from the running process or env file before any backup or migration step.
> Do NOT assume `/var/lib/naroom/naroom.db` or any other path.

---

## 0. Preflight checks (before any deploy)

```bash
# 1. Disk space — check actual data dirs, not assumed paths
df -h /opt/naroom /opt/naroom-web /opt/backups

# 2. Determine DB path from running service (do NOT guess; do NOT print other secrets):
DB_PATH=$(systemctl show naroom.service -p Environment | grep -oP 'DB_PATH=\K[^ ]+')
if [ -z "$DB_PATH" ]; then
  DB_PATH=$(grep -oP 'DB_PATH=\K.*' /opt/naroom/.env | head -1)
fi
# Abort if empty, not an absolute path, or file not found:
if [ -z "$DB_PATH" ] || [ "${DB_PATH:0:1}" != "/" ] || [ ! -f "$DB_PATH" ]; then
  echo "ERROR: DB_PATH is empty, not absolute, or file not found: '$DB_PATH'" >&2
  exit 1
fi
echo "DB_PATH=$DB_PATH"   # only the path is printed; no other secrets exposed

# 3. Active processes and ports
systemctl status naroom.service naroom-web.service
ss -tlnp | grep 8080

# 4. Current running binary fingerprint
sha256sum /opt/naroom/naroom

# 5. Current git commit (compare to local build SHA)
cat /opt/naroom/VERSION 2>/dev/null || echo "no VERSION file"

# 6. Running services
systemctl list-units --state=running | grep -E "naroom|caddy"

# 7. Caddy status + config
systemctl status caddy
cat /etc/caddy/Caddyfile   # or: caddy validate --config /etc/caddy/Caddyfile

# 8. Recent logs (last 50 lines, no PII expected)
journalctl -u naroom.service -n 50 --no-pager

# 9. DB size + integrity (use DB_PATH from step 2)
ls -lh "$DB_PATH"
sqlite3 "$DB_PATH" "PRAGMA integrity_check" | head -5
```

---

## 1. Backup (mandatory before any change)

```bash
# Determine DB path first (see §0 step 2):
DB_PATH=$(systemctl show naroom.service -p Environment | grep -oP 'DB_PATH=\K[^ ]+')
# or: grep DB_PATH /opt/naroom/.env

STAMP=$(date +%Y%m%d_%H%M%S)
BACKUP_DIR=/opt/backups/naroom

mkdir -p "$BACKUP_DIR"

# Hot backup DB (no service stop needed):
sqlite3 "$DB_PATH" ".backup $BACKUP_DIR/naroom.db.$STAMP"

# Backup current binary
cp /opt/naroom/naroom "$BACKUP_DIR/naroom.$STAMP"

# Backup frontend build
tar czf "$BACKUP_DIR/naroom-web.$STAMP.tar.gz" /opt/naroom-web/

# Backup env file (without printing values)
cp /opt/naroom/.env "$BACKUP_DIR/.env.$STAMP"
chmod 600 "$BACKUP_DIR/.env.$STAMP"
```

---

## 2. Build linux/amd64 binary locally

```bash
# On the build machine — must be in the exact reviewed V2 worktree.
# (NOT the main repo root; the V2 worktree contains the approved V2 code.)
cd /path/to/naroom-v2   # the codex/v2-foundation worktree

# Verify working tree is clean and HEAD matches the approved RC SHA:
git status --porcelain   # must produce NO output
git rev-parse HEAD       # must match the approved RC SHA exactly

# Build:
GOOS=linux GOARCH=amd64 go build -o naroom-linux-amd64 ./cmd/naroom

# Record the SHA of both the commit and the binary artifact:
RC_SHA=$(git rev-parse HEAD)
BINARY_SHA=$(sha256sum naroom-linux-amd64 | cut -d' ' -f1)
echo "RC_SHA=$RC_SHA"
echo "BINARY_SHA=$BINARY_SHA"
# Save these values — the operator must verify BINARY_SHA on the VPS before swap.

# Copy to VPS staging area:
scp naroom-linux-amd64 naroom-vps:/tmp/naroom-new
```

---

## 2a. Build and deploy frontend

```bash
# On the build machine — same V2 worktree as §2 (already verified clean + correct SHA).
cd /path/to/naroom-v2/frontend   # frontend/ subdirectory of the worktree

# Install dependencies (reproducible, no network drift after lock file):
npm ci

# Type-check + build:
npm run check
npm run build

# Copy to VPS staging area:
scp -r build/ naroom-vps:/tmp/naroom-web-new/
```

```bash
# On VPS — backup current frontend before swap:
STAMP=$(date +%Y%m%d_%H%M%S)
tar czf /opt/backups/naroom/naroom-web.$STAMP.tar.gz /opt/naroom-web/

# Swap frontend build:
rm -rf /opt/naroom-web/
cp -r /tmp/naroom-web-new/ /opt/naroom-web/

# Restart frontend service:
sudo systemctl restart naroom-web.service
sleep 3
systemctl status naroom-web.service
```

---

## 3. Staged deploy — Phase 1: V2_ENABLED=false (V1 only)

Deploy the new binary WITHOUT enabling V2. This validates V1 continuity.

```bash
# On VPS — verify checksum before swap (compare with BINARY_SHA from build machine):
sha256sum /tmp/naroom-new
# Must match BINARY_SHA; abort if different.

cp /tmp/naroom-new /opt/naroom/naroom-new
chmod 755 /opt/naroom/naroom-new

# Stop service
sudo systemctl stop naroom.service

# Swap binary
mv /opt/naroom/naroom /opt/naroom/naroom-old
mv /opt/naroom/naroom-new /opt/naroom/naroom

# Start with V2_ENABLED=false (no new env vars yet)
sudo systemctl start naroom.service
sleep 3
systemctl status naroom.service
```

### Phase 1 verification

```bash
# V1 health must return plaintext "ok" (no Content-Type, no JSON)
curl -i http://localhost:8080/health
# Expected: HTTP/1.1 200 OK  +  body: ok  +  no application/json Content-Type

# V2 routes must be absent
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/v2/health
# Expected: 404

curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8080/v2/client/payment-intents
# Expected: 404

# V1 board must still work
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/board/moscow
# Expected: 200 or 301 (redirect to frontend) — NOT 404 or 500
```

---

## 4. Set V2 secrets in environment file

**Do NOT print or log values.**

```bash
# Edit the env file directly on VPS (values never printed here):
sudo -u naroom vi /opt/naroom/.env

# Add the following keys (values generated with `openssl rand -hex 32`):
# V2_ENABLED=true
# V2_HMAC_KEY=<64 lowercase hex chars = 32 bytes>
# V2_CONTACT_ENC_KEY=<64 hex chars>
# V2_CONTACT_KEY_VERSION=v1
# V2_DEST_ENC_KEY=<64 hex chars>
# V2_DEST_KEY_VERSION=v1
# V2_CLIENT_BOT_TOKEN=<from BotFather — never logged>
# V2_CLIENT_BOT_NAME=<@botname without @>
# V2_CLIENT_WEBHOOK_SECRET=<32+ char random string>
# V2_INFORMER_BOT_TOKEN=<from BotFather>
# V2_INFORMER_BOT_NAME=<@botname without @>
# V2_INFORMER_WEBHOOK_SECRET=<32+ char random string>

# Verify file permissions:
ls -la /opt/naroom/.env
# Must be: -rw------- naroom naroom (or root-readable only)
```

---

## 5. Telegram bot setup

**Perform before enabling V2. Do NOT print tokens in terminal.**

```bash
# 5.1 Verify client bot identity (token NOT shown in output):
curl -s "https://api.telegram.org/bot${V2_CLIENT_BOT_TOKEN}/getMe" | jq '.result.username'

# 5.2 Set client bot webhook:
curl -s -X POST "https://api.telegram.org/bot${V2_CLIENT_BOT_TOKEN}/setWebhook" \
  -H "Content-Type: application/json" \
  -d "{\"url\":\"https://naroom.net/api/v2/telegram/client/webhook\",\"secret_token\":\"${V2_CLIENT_WEBHOOK_SECRET}\"}"
# Expected: {"ok":true,"result":true}

# 5.3 Verify client bot webhook:
curl -s "https://api.telegram.org/bot${V2_CLIENT_BOT_TOKEN}/getWebhookInfo" \
  | jq '{url:.result.url,pending_count:.result.pending_update_count}'

# 5.4 Set informer bot webhook:
curl -s -X POST "https://api.telegram.org/bot${V2_INFORMER_BOT_TOKEN}/setWebhook" \
  -H "Content-Type: application/json" \
  -d "{\"url\":\"https://naroom.net/api/v2/telegram/informer/webhook\",\"secret_token\":\"${V2_INFORMER_WEBHOOK_SECRET}\"}"

# 5.5 Verify informer bot webhook:
curl -s "https://api.telegram.org/bot${V2_INFORMER_BOT_TOKEN}/getWebhookInfo" \
  | jq '{url:.result.url,pending_count:.result.pending_update_count}'
```

---

## 6. Enable V2 and controlled restart

```bash
# Restart service (now with V2_ENABLED=true in secrets.env):
sudo systemctl restart naroom.service
sleep 5
systemctl status naroom.service
```

If the service exits immediately, check for fail-fast secret validation errors:

```bash
journalctl -u naroom.service -n 30 --no-pager | grep -i "v2wire\|FATAL\|fatal\|error"
```

---

## 7. V2 readiness verification

```bash
# V1 health still plaintext "ok":
curl -i http://localhost:8080/health
# Body: ok  (no JSON, no Content-Type change)

# V2 readiness:
curl -s http://localhost:8080/v2/health
# Expected: {"v2":"ready"}

# All V2 POST routes registered (must not 404):
for path in /v2/client/payment-intents /v2/client/listings/publish \
            /v2/informer/access /v2/helper/contact-purchases \
            /v2/helper/reviews /v2/telegram/client/webhook \
            /v2/telegram/informer/webhook; do
  code=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
    -H "Content-Type: application/json" -d '{}' http://localhost:8080${path})
  echo "$path → $code"
  # Any code OTHER than 404 means the route is registered.
done
```

```bash
# V2 workers must be running (check logs for "v2: lifecycle" / "v2: watcher"):
journalctl -u naroom.service -n 50 --no-pager | grep -E "v2:|watcher|lifecycle|informer"
```

---

## 8. V2 migration verification

```bash
# Use DB_PATH determined in §0 step 2:
DB_PATH=$(systemctl show naroom.service -p Environment | grep -oP 'DB_PATH=\K[^ ]+')
# or: DB_PATH=$(grep DB_PATH /opt/naroom/.env | cut -d= -f2)

# Schema must have V2 tables:
sqlite3 "$DB_PATH" ".tables" | tr ' ' '\n' | grep v2_ | sort

# Expected tables include:
# v2_client_flows  v2_client_invoices  v2_client_notification_bindings
# v2_telegram_destinations  v2_listings
# v2_helper_profiles  v2_helper_invoices  v2_helper_purchases
# v2_review_entitlements  v2_review_delivery_snapshots  v2_client_profiles
# v2_informer_tokens  v2_informer_subscriptions  v2_informer_destinations
# v2_informer_chat_index  v2_informer_outbox  v2_informer_outbox_recipients

# Verify §1 claim_token and lease_until columns exist (replaces old claimed_at model):
sqlite3 "$DB_PATH" "PRAGMA table_info(v2_informer_outbox)" | grep -E "claim_token|lease_until|claimed_by"

# Verify §2 attempts column on recipients:
sqlite3 "$DB_PATH" "PRAGMA table_info(v2_informer_outbox_recipients)" | grep attempts
```

---

## 9. Smoke paths (manual, no real crypto needed for structure check)

```bash
# V1 health — must remain plaintext "ok" at public URL:
curl -i https://naroom.net/api/health
# Expected: HTTP 200, body "ok", no application/json Content-Type

# GET board — must return HTML (not 404/500):
curl -s -o /dev/null -w "%{http_code}" https://naroom.net/board/tbilisi
# Expected: 200

# V2 health at public URL:
curl -s https://naroom.net/api/v2/health
# Expected: {"v2":"ready"}

# GET listing with fake ID — must return 404 (route registered, listing not found):
curl -s -o /dev/null -w "%{http_code}" https://naroom.net/api/v2/listings/fake-id
# Expected: 404 (handler-level, not routing miss)

# Informer access with missing body — must return 4xx (route registered):
curl -s -o /dev/null -w "%{http_code}" -X POST \
  -H "Content-Type: application/json" -d '{}' \
  https://naroom.net/api/v2/informer/access
# Expected: 400 or 422 (not 404 or 405)
```

---

## 10. Rollback procedure

If V2 causes issues, roll back in order:

```bash
# Step 1: Disable V2 flag (fastest, no binary swap needed)
# Edit env file: set V2_ENABLED=false
sudo -u naroom vi /opt/naroom/.env
sudo systemctl restart naroom.service
sleep 3

# Step 2: Verify V1 is back (plaintext "ok", no /v2/* routes)
curl -i http://localhost:8080/health
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/v2/health   # must be 404

# Step 3: If binary itself is broken, restore old binary:
sudo systemctl stop naroom.service naroom-web.service
mv /opt/naroom/naroom /opt/naroom/naroom-bad
mv /opt/naroom/naroom-old /opt/naroom/naroom
sudo systemctl start naroom.service naroom-web.service
curl -i http://localhost:8080/health   # must return plaintext "ok"

# Step 4: DB restore only if data corruption is confirmed (DESTRUCTIVE):
# Determine DB_PATH first (see §0 step 2), then:
# sqlite3 "$DB_PATH" ".restore /opt/backups/naroom/naroom.db.TIMESTAMP"
# Restore ONLY if the new binary wrote corrupted data AND you have confirmed this.
# V2 schema migration is additive; V1 data is unaffected.
```

### Post-rollback V1 checks

```bash
# V1 board still works:
curl -s -o /dev/null -w "%{http_code}" https://naroom.net/board/moscow
# Expected: 200

# V1 health contract:
curl -i https://naroom.net/health
# Expected: HTTP 200, body "ok", no JSON Content-Type

# No V2 routes:
curl -s -o /dev/null -w "%{http_code}" https://naroom.net/v2/health
# Expected: 404
```

---

## UNRESOLVED MANUAL GATES

The following require human operator action and cannot be automated here:

1. **Telegram `getMe` for production tokens** — tokens not provided; operator must
   verify bot identity manually using the commands in §5 above.

2. **Real BTC/LTC balance lookup on production addresses** — operator must
   provide at least one BTC address and one LTC address to verify that
   `mempool.space` and `BlockCypher` APIs return real balances.

3. **HD derivation test vector** — operator must verify that the first derived BTC
   and LTC addresses from the production xpub match expected values using
   `cmd/checkaddr` or equivalent.

4. **VPS Caddy `/api/v2/*` routing** — Caddyfile is at `/etc/caddy/Caddyfile`.
   Manual gate: verify that all `/api/v2/*` paths reverse-proxy to the naroom
   backend (not served as static files), then run `caddy validate --config
   /etc/caddy/Caddyfile` to confirm the config is syntactically correct.
