# SDK & Public Endpoints

These are the endpoints your **distributed application** calls. They're authenticated by the `license_key` itself — there is no API key to embed in (and leak from) your binary. All live under `/api/v1`.

!!! tip "Design principle"
    License gating happens at **app start** (`verify`), not on every action. Cache the returned signed token and verify it offline during its validity window — see [Offline Token Verification](offline-tokens.md). This keeps your app working through brief network outages and never bricks installed clients when a license rotates.

## Typical client flow

```text
first run   →  POST /license/activate   (register this device, get a token)
each start  →  POST /license/verify     (confirm still valid, refresh token)
per action  →  POST /license/usage      (metered features only)
offline     →  verify the cached token locally with the Ed25519 public key
```

## Activate

`POST /license/activate` — registers a device or user identifier against a license and returns a signed verification token.

```json
{ "license_key": "KG-XXXX-XXXX-XXXX-XXXX", "identifier": "device-a1b2c3", "identifier_type": "device", "label": "Work laptop" }
```

- **`identifier`** — a stable per-install fingerprint (device id) or a user email. This is what `verify` later checks against.
- Re-activating the same identifier returns `already_activated` **without** consuming another slot.
- `max_activations` is enforced **atomically** — concurrent retries can never exceed the cap.
- Supports an `Idempotency-Key` header (same key + body replays the cached response for 24h).

**Failure modes:** `400` validation · `403` license not usable (expired/suspended/revoked) · `404` not found · `409 ACTIVATION_LIMIT` slots exhausted · `429 LOCKED_OUT` brute-force lockout.

!!! important "Verify requires prior activation"
    `verify` only succeeds for an identifier that has been **activated**. A verify against an unregistered device collapses to `404 LICENSE_NOT_FOUND` (same as expired/revoked — deliberate [oracle hardening](../self-hosting/security.md#oracle-hardening)). Always `activate` once when you first receive a key, then `verify` per launch.

## Verify

`POST /license/verify` — checks the license is valid **and** the identifier is activated. Returns status, plan features, `support_until`, and a fresh signed token.

```json
{ "license_key": "KG-XXXX-…", "identifier": "device-a1b2c3" }
```

Response `data` includes `status`, `plan_id`, `plan_name`, `valid_until`, `support_until`, `features`, `grace_days`, `token`, and any `external_customer_id` / `external_workspace_id`. On any failure it returns `404 LICENSE_NOT_FOUND` (collapsed).

## Deactivate

`POST /license/deactivate` — frees an activation slot for an identifier (e.g. the user is moving to a new machine). Customers can also self-serve this from the portal.

## Entitlements

`POST /license/entitlements` — returns the resolved feature entitlements for a license without issuing a token. Useful for a lightweight capability check.

## Usage metering

For **quota** entitlements (see [Products & Plans](../concepts/products-plans.md#entitlements)):

`POST /license/usage` — records usage and atomically enforces the cap.

```json
{ "license_key": "KG-XXXX-…", "feature": "dxf_export", "quantity": 1 }
```

- **`200`** → returns `used`, `limit`, `remaining`, `period`, `period_key`. Show "N of M left today" in your UI.
- **`429 QUOTA_EXCEEDED`** → the cap is hit (details include `used`, `limit`, `period`). Block the action and prompt an upgrade.
- Enforcement is atomic at the database level — concurrent calls can't both slip past the limit.

`POST /license/usage/status` — reads current usage **without** consuming, so you can show remaining count before the user acts.

Counters reset automatically at the `quota_period` boundary (UTC). A `daily` counter keyed on the UTC date rolls over at 00:00 UTC.

## Software download

`POST /license/download` — returns a short-lived presigned URL for a release artifact, gated by the license and its [support window](../concepts/support-window.md). See [Software Updates](updates.md).

## Public key

`GET /license/pubkey` — the server's Ed25519 public key, fetched once by clients to verify tokens offline. See [Offline Token Verification](offline-tokens.md).

## Support renewal checkout

`POST /license/support/checkout` — starts a Stripe checkout that extends the license's support window on payment. Returns `{ checkout_url }`. See [Stripe → Support renewal](../billing/stripe.md#support-window-renewal).
