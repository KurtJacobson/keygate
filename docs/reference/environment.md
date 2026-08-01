# Environment Variables

The complete list, alphabetical. See [Configuration](../getting-started/configuration.md) for grouped explanations. `—` = no default (required or feature-off when unset).

## Core

| Variable | Default | Notes |
|----------|---------|-------|
| `DATABASE_URL` | — | **Required.** PostgreSQL DSN. |
| `JWT_SECRET` | — | **Required.** ≥32 chars. Signs admin sessions. |
| `LICENSE_SIGNING_KEY` | — | **Required.** 32-byte Ed25519 seed, 64 hex chars. |
| `ENVIRONMENT` | `development` | `development` \| `staging` \| `production`. |
| `BASE_URL` | — | Public URL, e.g. `https://license.example.com`. |
| `PORT` | `9000` | HTTP listen port. |
| `ADMIN_EMAILS` | — | Comma-separated; auto-promoted to admin, bypass OTP existing-user gate. |

## Auth & rate limiting

| Variable | Default | Notes |
|----------|---------|-------|
| `OTP_REQUIRE_EXISTING_USER` | `false` | Only send OTP to existing accounts. |
| `BF_MAX_FAILS` | `5` | Failures before brute-force lockout. |
| `BF_LOCKOUT_SECONDS` | `30` | Lockout duration. |
| `RATE_LIMIT_API` | `60` | Per-IP req/min, general API. |
| `RATE_LIMIT_AUTH` | — | Per-IP req/min, auth endpoints. |

## Email (SMTP)

| Variable | Default | Notes |
|----------|---------|-------|
| `SMTP_HOST` | — | SMTP server. |
| `SMTP_PORT` | — | `587` (STARTTLS) or `465`. |
| `SMTP_USERNAME` | — | Auth user. |
| `SMTP_PASSWORD` | — | Auth password. |
| `SMTP_FROM` | — | Sender; bare address or `Name <addr>`. |

## Stripe

| Variable | Default | Notes |
|----------|---------|-------|
| `STRIPE_SECRET_KEY` | — | `sk_test_…` / `sk_live_…`. |
| `STRIPE_WEBHOOK_SECRET` | — | Empty → auto-register webhook. |
| `STRIPE_LIVEMODE` | derived | Overrides key-prefix detection. |

## Object storage & encryption

| Variable | Default | Notes |
|----------|---------|-------|
| `STORAGE_BUCKET` | — | Enables software distribution (with the two keys below). |
| `STORAGE_ACCESS_KEY` | — | S3 access key. |
| `STORAGE_SECRET_KEY` | — | S3 secret key. |
| `STORAGE_ENDPOINT` | — | For R2/MinIO/non-AWS. |
| `STORAGE_REGION` | — | S3 region. |
| `STORAGE_FORCE_PATH_STYLE` | `false` | `true` for MinIO/path-style. |
| `STORAGE_UPLOAD_TTL` | service default | Presigned upload lifetime (Go duration). |
| `STORAGE_DOWNLOAD_TTL` | `10m` | Presigned download lifetime. |
| `RELEASE_KEY_ENCRYPTION_KEY` | — | AES-256 master key (64 hex). Required when storage is enabled; also encrypts license keys at rest. |

## Webhooks (outbound delivery tuning)

| Variable | Default | Notes |
|----------|---------|-------|
| `WEBHOOK_HTTP_TIMEOUT` | `10s` | Per-delivery timeout. |
| `WEBHOOK_RETRY_INTERVAL` | `30s` | Retry loop interval. |
| `WEBHOOK_MAX_ATTEMPTS` | — | Max delivery attempts. |

## Misc

| Variable | Default | Notes |
|----------|---------|-------|
| `QUOTA_WARNING_THRESHOLD` | — | Fraction (e.g. `0.8`) at which `quota.warning` fires. |
| `REDIS_URL` | — | Optional Redis backend for rate limiting (off by default; in-memory otherwise). |

!!! note
    Defaults reflect the reference build; confirm against your version's `internal/config` if a value is load-bearing for you. Names are read from the process environment or a `.env` file — plain `KEY=value`, no `export`, no quotes.
