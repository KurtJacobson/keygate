# Keygate

**Self-hosted software license management.** Keygate is a single Go binary plus PostgreSQL that handles the full lifecycle of software licensing — key issuance, device activation, offline verification, usage quotas, software update distribution, team seats, and Stripe billing — all behind one admin dashboard, running on your own infrastructure.

!!! info "What this documentation covers"
    Installing and configuring Keygate, the concepts behind products/plans/licenses, integrating a client application against the SDK, wiring up Stripe, distributing signed updates, and operating a self-hosted deployment. If you just want to run it, start with **[Installation](getting-started/installation.md)**.

## Why Keygate

Commercial licensing platforms charge per seat and hold your customer data on their servers. Building your own means months of work — activation logic, payment reconciliation, quota tracking, and every 2 a.m. edge case. Keygate is the middle path: a production-ready license server on your own infrastructure, wired to your own Stripe account.

## Core capabilities

| Area | What you get |
|------|--------------|
| **Licensing models** | Subscriptions, perpetual, trials, floating (concurrent), and perpetual + paid support — see [Licensing Models](concepts/licensing-models.md) |
| **Activation** | Per-device or per-user activation with atomic limit enforcement; offline verification via signed Ed25519 tokens |
| **Usage metering** | Atomic quota enforcement (hourly/daily/monthly/yearly) with threshold warnings |
| **Software distribution** | Signed release feeds for Sparkle, Velopack, and Tauri; license-gated downloads |
| **Billing** | End-to-end Stripe: checkout, webhooks, dunning, refunds, and support-window renewal |
| **Team seats** | Customer-managed teams with roles and per-plan seat limits |
| **Administration** | Products, plans, licenses, customers, API keys, webhooks, analytics, audit logs, email templates |

## How the pieces fit

```text
                    ┌─────────────────────────────┐
   Your app / SDK ──┤  Public SDK  (license_key)  │
   (desktop, CLI)   │  activate · verify · usage  │
                    │  download · support/checkout│
                    └──────────────┬──────────────┘
                                   │
   Your backend ────┤ Admin API (Bearer kg_live_) ├──► Keygate ──► PostgreSQL
   (Stripe hooks,   │  licenses · plans · products │       │
    automation)     └──────────────────────────────┘      │
                                                            ▼
   Stripe ──────────► /webhook/stripe ──────────────► license created / renewed
```

- **Public SDK endpoints** are authenticated by the `license_key` itself — nothing secret is embedded in your distributed binary. See [SDK & Public Endpoints](integration/sdk.md).
- **Admin endpoints** require a `kg_live_…` API key (or an admin session) and drive everything under `/admin/*`. See [Admin API & API Keys](administration/admin-api.md).
- **Stripe** creates and renews licenses automatically through the webhook. See [Stripe Integration](billing/stripe.md).

## Deployment shape

One binary, one database, optional S3-compatible object storage for release artifacts. No Redis, no microservices. Auto-migrates on startup, ships a first-run setup wizard, and supports custom branding, email templates, and English/Chinese UI. See [Deployment](self-hosting/deployment.md).

## Next steps

<div class="grid cards" markdown>

- :material-download: **[Install Keygate](getting-started/installation.md)** — Docker or from source
- :material-cog: **[Configure it](getting-started/configuration.md)** — every environment variable
- :material-book-open-variant: **[Understand the model](concepts/products-plans.md)** — products, plans, entitlements
- :material-application-brackets: **[Integrate a client](integration/sdk.md)** — the SDK your app calls

</div>
