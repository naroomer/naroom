# Self-Hosting Guide — NA Room

This guide is for operators who want to run their own instance of NA Room.

## Prerequisites

- Linux server (Debian/Ubuntu recommended)
- Go 1.21+
- SQLite3
- A Bitcoin xpub (BIP32 extended public key) for generating invoice addresses
- A Litecoin xpub for generating invoice addresses
- Optional: Tor for hidden service

## Required Environment Variables

```bash
# Mandatory
SERVER_SALT=<openssl rand -hex 32>   # Context salt (keep offline, never in Git)
HASH_KEY=<openssl rand -hex 32>      # HMAC key for wallet address hashing (separate from SERVER_SALT)
BTC_XPUB=xpub...                     # BIP32 xpub for generating BTC invoice addresses
LTC_XPUB=Ltub...                     # BIP32 xpub for generating LTC invoice addresses

# Optional
PORT=8080
DB_PATH=/var/lib/naroom/naroom.db
MEMPOOL_API=https://mempool.space/api
BLOCKCYPHER_API=https://api.blockcypher.com/v1/ltc/main
BALANCE_CHECK_INTERVAL=600           # seconds
TTL_CLEAN_INTERVAL=60
INVOICE_WATCH_INTERVAL=30
LISTING_TTL=86400                    # 24 hours (default; override to customise visibility window)
CHAT_TTL=86400                       # 24 hours
```

Never put these in `.env` files in the repository. Never commit them to Git. Inject at startup.

## Generating Secrets

```bash
# Generate SERVER_SALT
openssl rand -hex 32

# Generate HASH_KEY (separate key — do not reuse SERVER_SALT)
openssl rand -hex 32
```

Store both values in a password manager or hardware-backed secret store. Keep offline backups.

## Build and Run

```bash
git clone https://github.com/naroomer/naroom
cd naroom
go build -o naroom ./cmd/naroom

# Run (inject secrets at runtime, never in a file)
SERVER_SALT=... HASH_KEY=... BTC_XPUB=... LTC_XPUB=... ./naroom
```

## Server Hardening

Run these commands on a fresh Debian/Ubuntu server:

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y ufw fail2ban unattended-upgrades

# Firewall
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp      # SSH — change to non-standard port if preferred
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable

# Disable swap (reduces risk of secrets being written to disk)
sudo swapoff -a
sudo sed -i '/swap/d' /etc/fstab

# Disable core dumps
sudo systemctl mask systemd-coredump
echo '* hard core 0' | sudo tee -a /etc/security/limits.conf
```

SSH hardening — edit `/etc/ssh/sshd_config`:

```
PermitRootLogin no
PasswordAuthentication no
PubkeyAuthentication yes
X11Forwarding no
AllowTcpForwarding no
```

```bash
sudo systemctl restart sshd
```

## Systemd Service

Create `/etc/systemd/system/naroom.service`:

```ini
[Unit]
Description=NA Room backend
After=network.target

[Service]
Type=simple
User=naroom
WorkingDirectory=/opt/naroom
ExecStart=/opt/naroom/naroom
Restart=always
RestartSec=5

# Do NOT put secrets here — inject them at runtime or use EnvironmentFile
# with strict permissions (chmod 600, owned by naroom user)
# EnvironmentFile=/etc/naroom/secrets.env

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/naroom

[Install]
WantedBy=multi-user.target
```

If you use an EnvironmentFile, set strict permissions:

```bash
sudo chmod 600 /etc/naroom/secrets.env
sudo chown naroom:naroom /etc/naroom/secrets.env
```

Note: environment files stored on disk are visible to root. If you want stronger protection against server seizure, inject secrets manually at startup rather than storing them in a file.

## Tor Hidden Service

Install Tor:

```bash
sudo apt install -y tor
```

Add to `/etc/tor/torrc`:

```
HiddenServiceDir /var/lib/tor/naroom/
HiddenServicePort 80 127.0.0.1:8080
```

```bash
sudo systemctl restart tor
sudo cat /var/lib/tor/naroom/hostname   # your .onion address
```

Back up the private key at `/var/lib/tor/naroom/hs_ed25519_secret_key` securely. This is your .onion identity — losing it means losing the address.

## Nginx (Optional — for Clearnet TLS)

If you also want a clearnet HTTPS endpoint:

```nginx
server {
    listen 443 ssl;
    server_name naroom.example.com;

    ssl_certificate     /etc/letsencrypt/live/naroom.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/naroom.example.com/privkey.pem;

    # No access logs — do not log user paths or query strings
    access_log off;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP "";           # strip IP — backend does not want it
        proxy_set_header X-Forwarded-For "";     # strip — never log IPs
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";   # WebSocket support
        proxy_read_timeout 3600;
    }
}

server {
    listen 80;
    server_name naroom.example.com;
    return 301 https://$host$request_uri;
}
```

## Logging Policy

NA Room logs at the route-pattern level only:

```
listing.create OK
chat.close OK
balance_checker: checked 3 wallets
```

It does **not** log:
- IP addresses
- Query strings
- Authorization headers
- Wallet addresses or hashes
- Invoice addresses
- WebSocket subprotocol headers (which carry session tokens)
- Request bodies

Keep systemd journal retention short or disable it for sensitive deployments:

```bash
sudo journalctl --vacuum-time=7d
```

## What To Never Do

- Never commit `SERVER_SALT` or `HASH_KEY` to Git.
- Never put secrets in Docker environment files that are committed to a repository.
- Never enable `DEV_MODE=true` in production.
- Never reuse `SERVER_SALT` as `HASH_KEY` if you can avoid it — use two separate secrets.
- Never run with placeholder xpubs — real payments will fail silently.
- Never serve Tor users clearnet resources.
- Never enable analytics or external frontend resources.
- Never claim payments are anonymous — they are pseudonymous at best.

## Data the Operator Should Not Collect

By design, the server does not collect these. Make sure your infrastructure does not add them back:

- IP addresses (no nginx access logs, no upstream logging)
- Browser fingerprints
- Analytics identifiers
- Session identifiers in URLs
- Plaintext wallet addresses in logs

## Key Rotation

If `HASH_KEY` is ever compromised:

1. Generate a new `HASH_KEY`.
2. All existing wallet hashes in the database become unlinkable with the new key.
3. All active sessions will fail (they contain hashes computed with the old key) — users must re-authenticate.
4. Restart the server with the new key.

There is no automated migration — this is intentional. A key rotation is a hard break.
