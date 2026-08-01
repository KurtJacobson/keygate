# API Reference

The full machine-readable contract for the **public SDK endpoints** lives in [`openapi.yaml`](../openapi.yaml) in this repository — import it into Postman, Insomnia, Swagger UI, or an OpenAPI code generator. This page is the human-readable index.

## Base path

All endpoints are under `/api/v1`. Checkout redirects (`/pay/...`, `/checkout/...`) are at the root.

## Public SDK (license_key-authenticated)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/license/pubkey` | Ed25519 public key for offline token verification. |
| `POST` | `/license/activate` | Register a device/user identifier; returns a signed token. |
| `POST` | `/license/verify` | Confirm validity + activation; returns a fresh token. |
| `POST` | `/license/deactivate` | Free an activation slot. |
| `POST` | `/license/entitlements` | Resolve feature entitlements. |
| `POST` | `/license/usage` | Record + enforce a quota. |
| `POST` | `/license/usage/status` | Read quota without consuming. |
| `POST` | `/license/download` | License-gated presigned download URL. |
| `POST` | `/license/support/checkout` | Start a Stripe support-window renewal. |
| `POST` | `/license/floating/checkout` · `/heartbeat` · `/checkin` | Floating (concurrent) sessions. |
| `GET` | `/releases/{slug}/feed.xml` · `feed.json` · `upgrade.json` | Public update feeds (Sparkle/Velopack/Tauri). |

## Auth

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/auth/providers` | Which login methods are enabled. |
| `POST` | `/auth/otp/send` · `/otp/verify` | Email OTP login. |
| `POST` | `/auth/dev-login` | Dev only (404 in production / non-localhost). |
| `POST` | `/auth/logout` · `/refresh` | Session management. |

## Billing (Stripe)

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/webhook/stripe` | Inbound Stripe events. |
| `GET` | `/checkout/verify` | Success-page verification. |
| `GET` | `/pay/{checkout_id}` | Hosted checkout redirect (root path). |

## Admin (`Authorization: Bearer kg_live_…`)

All under `/admin`. Highlights (see [Admin API](../administration/admin-api.md) for the full set):

| Method | Path | Purpose |
|--------|------|---------|
| `GET` `POST` | `/admin/products` · `/admin/plans` | Catalog management. |
| `GET` `POST` | `/admin/licenses` | List / issue licenses. |
| `POST` | `/admin/licenses/{id}/revoke` · `/suspend` · `/reinstate` · `/change-plan` | Lifecycle. |
| `POST` | `/admin/licenses/{id}/valid-until` · `/support-until` | Set expiry / support window. |
| `GET` `POST` | `/admin/webhooks` | Outbound webhook config. |
| `GET` `POST` | `/admin/api-keys` | Server-to-server credentials. |
| `POST` | `/admin/system/run-expiry-checks` · `/run-metered-sync` | On-demand jobs. |

## Response envelope

Responses wrap data as:

```json
{ "success": true, "data": { ... } }
```

Errors as:

```json
{ "success": false, "error": { "code": "SUPPORT_EXPIRED", "message": "…" } }
```

Consistent HTTP status codes accompany the envelope (`400`, `403`, `404`, `409`, `429`, `503`). Note the deliberate `404 LICENSE_NOT_FOUND` collapse on public license endpoints — see [Security → Oracle hardening](../self-hosting/security.md#oracle-hardening).
