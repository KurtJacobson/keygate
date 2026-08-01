# Installation

Keygate runs as a single Go binary against a PostgreSQL database. The quickest path is Docker Compose; you can also build from source.

## Requirements

- **PostgreSQL 14+** (the official image uses `postgres:18-alpine`).
- **A public HTTPS URL** for production — Stripe webhooks and license clients can't reach `localhost`. Put a reverse proxy (Caddy, nginx, Traefik) in front for TLS. See [Deployment](../self-hosting/deployment.md).
- **(Optional) S3-compatible object storage** — only if you distribute software updates (Cloudflare R2, AWS S3, MinIO). Licensing works without it.

## Docker Compose (recommended)

```bash
# 1. Fetch the compose file and env template
curl -O https://raw.githubusercontent.com/kurtjacobson/keygate/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/kurtjacobson/keygate/main/.env.example
cp .env.example .env

# 2. Generate the two mandatory secrets
openssl rand -hex 32   # → JWT_SECRET
openssl rand -hex 32   # → LICENSE_SIGNING_KEY
# edit .env and paste them in

# 3. Start
docker compose up -d
```

Keygate serves on port **9000** by default. Open `http://localhost:9000` (or your domain) to reach the first-run setup wizard.

!!! warning "PostgreSQL 18 volume layout"
    The `postgres:18` image expects its data volume mounted at `/var/lib/postgresql` (not `.../data`). The bundled `docker-compose.yml` already does this. If you adapted an older compose file and see *"there appears to be PostgreSQL data in /var/lib/postgresql/data (unused mount/volume)"*, fix the mount path.

### Pulling the image

The image is published to GitHub Container Registry:

```bash
docker pull ghcr.io/kurtjacobson/keygate:latest
```

Tagged releases are also available as `:X.Y.Z`. See [Maintenance & Updates](../self-hosting/maintenance.md) for the release/tag workflow.

## From source

```bash
git clone https://github.com/kurtjacobson/keygate.git
cd keygate
cp .env.example .env       # set JWT_SECRET + LICENSE_SIGNING_KEY
make build && ./bin/keygate
```

Requires Go 1.25+. Migrations run automatically on startup from `db/migrations`.

## What happens on first start

1. **Migrations apply** — the schema is created/updated automatically (advisory-locked, so multiple instances won't race).
2. **Security validation** — Keygate refuses to start if `JWT_SECRET` is too short or `LICENSE_SIGNING_KEY` isn't a valid 32-byte hex seed. Weak-secret checks and other warnings print to the log.
3. **Setup wizard** — the first visit walks you through creating the initial admin.

## The two mandatory secrets

| Variable | What it is | Generate with |
|----------|-----------|---------------|
| `JWT_SECRET` | Signs admin session tokens. Min 32 chars. | `openssl rand -hex 32` |
| `LICENSE_SIGNING_KEY` | 32-byte Ed25519 **seed** (64 hex chars). Signs the offline verification tokens your clients trust. | `openssl rand -hex 32` |

!!! danger "Back up `LICENSE_SIGNING_KEY`"
    This key signs every license token your clients verify offline. If you lose it and generate a new one, **every previously issued token becomes unverifiable**. Store it somewhere durable (a password manager), separate from the server.

Continue to **[Configuration](configuration.md)** for the full environment-variable reference, or jump to **[First concepts](../concepts/products-plans.md)**.
