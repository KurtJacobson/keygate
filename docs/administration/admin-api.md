# Admin API & API Keys

Everything under `/admin/*` manages your catalog and licenses. It's reachable two ways: an **admin session** (the dashboard) or a **server-to-server API key** (`Authorization: Bearer kg_live_…`) for automation — minting licenses from your own backend, nightly exports, etc.

## API keys

Mint keys in the dashboard under **API Keys**. Each key has:

- **Scopes** — what it may do (fail-closed: a key with no scopes can do nothing).
- **Product binding** *(optional)* — bound to one product, or system-wide (unbound). A product-bound key is rejected on requests for a different product (`403 PRODUCT_SCOPE_MISMATCH`).

### Scopes

| Scope | Grants |
|-------|--------|
| `admin` | Full `/admin/*` access — equivalent to a logged-in admin. |
| `licenses:write` | Create/update/manage licenses (checkout backends, quota services). |
| `releases:write` | Push releases and artifacts (CI/CD). |

Use the narrowest scope that does the job — same model as Stripe `sk_live_` keys and GitHub PATs. Keys are shown once at creation; store them like any secret.

!!! warning "Never ship an API key in a distributed binary"
    Any `licenses:write` (or `admin`) key can mint licenses. In a desktop app it's extractable. Client apps use the [public SDK](../integration/sdk.md) (license_key-authed); only *your servers* hold API keys.

## Key license endpoints

Authenticate with `Authorization: Bearer kg_live_…`. Common operations:

| Endpoint | Purpose |
|----------|---------|
| `POST /admin/licenses` | Issue a license (`product_id`, `plan_id`, `email`; optional `valid_until`, `support_until`, `external_customer_id`). |
| `GET /admin/licenses` | List/filter (by `product_id`, `status`, `search`, `external_customer_id`, …). |
| `GET /admin/licenses/{id}` | Fetch one. |
| `POST /admin/licenses/{id}/revoke` \| `/suspend` \| `/reinstate` | Lifecycle actions. |
| `POST /admin/licenses/{id}/change-plan` | Move a license to another plan. |
| `POST /admin/licenses/{id}/valid-until` | Set/clear the expiry date. |
| `POST /admin/licenses/{id}/support-until` | Set/clear the [support window](../concepts/support-window.md). |
| `POST /admin/licenses/{id}/refund` | Refund via the payment provider and revoke. |
| `GET /admin/licenses/{id}/usage` · `POST …/usage/reset` | View/reset quota counters. |

Plans and products are managed under `/admin/plans` and `/admin/products`; webhooks under `/admin/webhooks`; settings under `/admin/settings`.

## Discovering IDs

The admin API and dashboard expose product/plan IDs (UUIDs). A quick way to list them:

```bash
curl -H "Authorization: Bearer kg_live_…" https://<base-url>/api/v1/admin/products
curl -H "Authorization: Bearer kg_live_…" "https://<base-url>/api/v1/admin/plans?product_id=<uuid>"
```

Use the real UUID `id` values — plan/product **slugs** are not the IDs the API matches on.

## On-demand jobs

- `POST /admin/system/run-expiry-checks` — runs the full lifecycle pass immediately (expire trials/grace, send reminders/dunning/support emails). Useful for testing time-based behavior without waiting for the hourly cron.
- `POST /admin/system/run-metered-sync` — drains the Stripe meter-event queue now.

## The dashboard

The admin dashboard exposes all of the above visually — products, plans, licenses, customers, API keys, webhooks, analytics, audit logs, team management, email templates, and branding — with search, filter, and CSV/JSON export. Admin login is **email OTP** (so SMTP must work), with role-based access checked per request.
