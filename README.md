# Keygate

**Open source, self-hosted software license management.**

A single Go binary + PostgreSQL that handles license keys, activation, payments, usage metering, and signed auto-updates — the self-hosted alternative to Keygen, Cryptlex, and LicenseSpring.

[![License](https://img.shields.io/badge/license-AGPL%20v3-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/kurtjacobson/keygate?label=release&color=green)](https://github.com/kurtjacobson/keygate/releases)

📖 **[Documentation](https://kurtjacobson.github.io/keygate/)**

## Capabilities

- **License management** — subscriptions, perpetual, trials, floating (concurrent), and perpetual + paid-support licenses. Activate, verify, suspend, revoke with full audit trail and atomic activation limits.
- **Offline verification** — Ed25519-signed tokens; public SDK endpoints take the license key directly, so no API keys are embedded in your binaries.
- **Air-gapped licensing** — issue perpetual, machine-bound license files from the admin panel for devices that never touch the internet; verified fully offline.
- **Perpetual + paid support** ("perpetual fallback" / JetBrains-style) — the license never expires, but a separate support window gates access to newer releases. Lapsed customers keep every release published while their support was active and renew (self-serve via Stripe) to get newer ones.
- **Software distribution** — signed release feeds for Sparkle (macOS), Velopack (Windows), and Tauri; S3-compatible storage; license-gated downloads.
- **Usage metering** — track any metric with atomic, database-level quota enforcement and threshold webhooks.
- **Payments** — end-to-end Stripe integration: checkout, proration, dunning, refunds, billing portal.
- **Team seats & entitlements** — customer-managed teams, seat roles, and feature flags / limits / quotas.
- **Server-to-server API** — scoped `kg_live_` bearer keys with fail-closed authorization.
- **Admin dashboard** — products, plans, licenses, customers, webhooks, analytics, audit logs, and branding.
- **Self-hosted** — one binary, one database, no Redis. Auto-migration and a first-run setup wizard.

## Quick Start

```bash
# Download
curl -O https://raw.githubusercontent.com/kurtjacobson/keygate/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/kurtjacobson/keygate/main/.env.example
cp .env.example .env

# Set JWT_SECRET and LICENSE_SIGNING_KEY (openssl rand -hex 32), then run
docker compose up -d
```

Open **http://localhost:9000** and the setup wizard takes it from there. Full deployment guides and SDK examples are in the [documentation](https://kurtjacobson.github.io/keygate/).

## License

[AGPL v3](LICENSE) with additional terms per [Section 7(b)](https://www.gnu.org/licenses/agpl-3.0.en.html#section7) — Copyright © 2026 [Tabloy](https://tabloy.app).

You are free to fork, modify, and self-host under the AGPL v3. The **"Powered by Keygate"** attribution in the UI must be preserved (see [NOTICE](NOTICE)). A commercial license to remove it is available — contact [hello@keygate.app](mailto:hello@keygate.app).
