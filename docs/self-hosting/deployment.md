# Deployment

A production Keygate deployment is: the container(s), a reverse proxy for TLS, and a public DNS name. Below is a concrete recipe on a small VPS.

## Sizing

Keygate is lightweight — a Go binary plus PostgreSQL. A **1 GB / 1 vCPU** VPS runs it comfortably for low-to-moderate volume. PostgreSQL is the main memory consumer; the Keygate process itself is tens of MB.

!!! warning "Don't build on a tiny box"
    Compiling the frontend needs more RAM than a 1 GB VPS has (it OOM-kills). **Pull the prebuilt image** (`ghcr.io/kurtjacobson/keygate:latest`) rather than building on the server. Add a 2 GB swap file as a safety net regardless.

## Recipe (Ubuntu + Docker + Caddy)

### 1. Harden the box

```bash
apt update && apt upgrade -y
adduser deploy && usermod -aG sudo deploy
apt install -y ufw
ufw allow OpenSSH && ufw allow 80/tcp && ufw allow 443/tcp && ufw enable
apt install -y unattended-upgrades
```

### 2. Install Docker + swap

```bash
curl -fsSL https://get.docker.com | sh
usermod -aG docker deploy
fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab
```

### 3. DNS

Point an A record (e.g. `license.example.com`) at the VPS IP.

### 4. Deploy Keygate

```bash
mkdir -p /opt/keygate && cd /opt/keygate
curl -fsSLO https://raw.githubusercontent.com/kurtjacobson/keygate/main/docker-compose.yml
curl -fsSL https://raw.githubusercontent.com/kurtjacobson/keygate/main/.env.example -o .env
# edit .env: JWT_SECRET, LICENSE_SIGNING_KEY, BASE_URL, ENVIRONMENT=production, SMTP_*, etc.
docker compose up -d
```

### 5. TLS with Caddy

```bash
apt install -y caddy
```

`/etc/caddy/Caddyfile`:

```text
license.example.com {
    reverse_proxy localhost:9000
}
```

```bash
systemctl reload caddy
```

Caddy provisions a Let's Encrypt certificate automatically on first request. Browse to `https://license.example.com` for the setup wizard.

## Production checklist

- [ ] `ENVIRONMENT=production` (disables dev-login, hardens cookies/CORS).
- [ ] `BASE_URL` set to the public HTTPS URL.
- [ ] `ADMIN_EMAILS` includes your address (so you can log in as admin).
- [ ] SMTP configured (admin login is OTP — you can't sign in without it).
- [ ] `LICENSE_SIGNING_KEY` backed up off-server.
- [ ] Off-box database backups scheduled (see [Maintenance](maintenance.md)).
- [ ] Firewall on; only 22/80/443 open.
- [ ] `restart: unless-stopped` (or `always`) on both containers so they survive reboots — verify with a `reboot` test.

## Reverse proxy notes

Any TLS-terminating proxy works (Caddy, nginx, Traefik). Keygate serves plain HTTP on `:9000`; the proxy handles certificates. If you run multiple sites on one box, give each its own server block — Keygate coexists fine alongside other services.
