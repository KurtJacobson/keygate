# Configuration

Keygate is configured entirely through environment variables (read from the process environment or a `.env` file). This page groups them by function; the [Environment Variables reference](../reference/environment.md) is the flat A–Z table.

## Required

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL connection string, e.g. `postgres://keygate:pass@db:5432/keygate?sslmode=disable`. |
| `JWT_SECRET` | Signs admin session tokens. **Minimum 32 characters** — startup fails otherwise. |
| `LICENSE_SIGNING_KEY` | 32-byte Ed25519 seed as 64 hex chars (`openssl rand -hex 32`). Signs offline license tokens. |

## Environment & networking

| Variable | Default | Description |
|----------|---------|-------------|
| `ENVIRONMENT` | `development` | `development`, `staging`, or `production`. **Set to `production` for live deployments** — see the box below. |
| `BASE_URL` | — | The public URL Keygate is served at, e.g. `https://license.example.com`. Used for Stripe return URLs, webhook auto-setup, and seat-invite links. |
| `PORT` | `9000` | HTTP listen port. |
| `ADMIN_EMAILS` | — | Comma-separated emails auto-promoted to admin on login. Bootstraps the first admin and bypasses the OTP existing-user gate. |

!!! danger "`ENVIRONMENT=production` matters"
    In `development`, Keygate enables **dev-login** (log in as any email with no OTP), omits the `Secure` flag on session cookies, loosens CORS, and runs Gin in debug mode. Running a public server in `development` is a serious hole. Always set `ENVIRONMENT=production` for anything internet-facing. (Dev-login has a second guard: it 404s unless `BASE_URL` contains `localhost`.)

## Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `OTP_REQUIRE_EXISTING_USER` | `false` | When `true`, email OTP codes are only sent to addresses that already have an account (admins in `ADMIN_EMAILS` bypass this). Closes open self-signup. Unknown emails get an identical "sent" response so the endpoint can't be used to enumerate accounts. |
| `BF_MAX_FAILS` | `5` | Failed attempts before brute-force lockout (activation, OTP). |
| `BF_LOCKOUT_SECONDS` | `30` | Lockout duration after `BF_MAX_FAILS`. |
| `RATE_LIMIT_API` | `60` | Per-IP requests/minute on general API endpoints. |
| `RATE_LIMIT_AUTH` | — | Per-IP requests/minute on auth endpoints. |

## Email (SMTP)

Email delivers OTP login codes, license keys, expiry/renewal reminders, and dunning notices. Without it, OTP codes are printed to the log (dev only).

| Variable | Description |
|----------|-------------|
| `SMTP_HOST` | SMTP server hostname. |
| `SMTP_PORT` | Usually `587` (STARTTLS) or `465`. Many VPS providers block outbound `25`. |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | Credentials. Keygate negotiates PLAIN or LOGIN auth based on what the server advertises after STARTTLS. |
| `SMTP_FROM` | Sender address. May be a bare address or `Name <addr@example.com>` (the display-name form is stripped for the SMTP envelope). |

Admin login uses **email OTP**, so working SMTP is effectively required to sign in — set it before you rely on the dashboard.

## Stripe (optional)

| Variable | Description |
|----------|-------------|
| `STRIPE_SECRET_KEY` | `sk_test_…` or `sk_live_…`. Enables checkout, webhooks, and metered billing sync. |
| `STRIPE_WEBHOOK_SECRET` | Leave **empty** and Keygate auto-registers the webhook endpoint (when `BASE_URL` is public). Set it manually only if you create the endpoint yourself. |
| `STRIPE_LIVEMODE` | Auto-derived from the key prefix (`sk_live_` → live). Set explicitly to override. Webhook events whose livemode doesn't match are rejected. |

See [Stripe Integration](../billing/stripe.md).

## Object storage (optional — software distribution only)

Required only if you distribute software updates. If any `STORAGE_*` field is set, all three of bucket/access-key/secret-key must be set (partial config fails at startup).

| Variable | Description |
|----------|-------------|
| `STORAGE_BUCKET` | Bucket name. |
| `STORAGE_ACCESS_KEY` / `STORAGE_SECRET_KEY` | S3 credentials. |
| `STORAGE_ENDPOINT` | For non-AWS providers (R2, MinIO). |
| `STORAGE_REGION` | S3 region. |
| `STORAGE_FORCE_PATH_STYLE` | `true` for MinIO / path-style endpoints. |
| `STORAGE_UPLOAD_TTL` / `STORAGE_DOWNLOAD_TTL` | Presigned URL lifetimes (Go duration, e.g. `10m`). |
| `RELEASE_KEY_ENCRYPTION_KEY` | AES-256 master key (64 hex chars). Encrypts license keys and per-product release-signing private keys at rest. **Required when storage is enabled.** Without it, license keys are stored in plaintext (a startup warning fires). |

See [Software Updates](../integration/updates.md).

## Applying changes

Environment variables are read at startup and **baked into the container at creation**. After editing `.env`:

```bash
docker compose up -d      # recreates the container with new env (a plain `restart` won't re-read .env)
```
