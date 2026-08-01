# Security

Keygate is designed to be internet-facing. This page covers its built-in protections and the operational settings you're responsible for.

## Built-in protections

- **Email OTP login** with constant-time hash verification; role-based access checked per request from the database.
- **Brute-force protection** — per-IP exponential lockout on activation and OTP (`BF_MAX_FAILS` / `BF_LOCKOUT_SECONDS`).
- **Rate limiting** — per-IP on API and auth endpoints.
- **License keys** SHA-256 hashed and (with `RELEASE_KEY_ENCRYPTION_KEY`) AES-256-GCM encrypted at rest.
- **Signed webhooks** — HMAC signatures on outbound deliveries.
- **HMAC/HS256-pinned JWTs**, SameSite cookies, HSTS.
- **Idempotency-Key middleware** — retried writes never double-execute.
- **Startup validation** — refuses to boot on weak secrets (short `JWT_SECRET`, malformed signing key).

## Oracle hardening

The public license endpoints deliberately collapse **every** "license-knowable" failure — doesn't exist, wrong product, wrong/unregistered device, suspended, revoked, expired — into a single `404 LICENSE_NOT_FOUND`. This closes off `license_key` enumeration: an attacker can't distinguish "no such key" from "valid key, wrong device."

A practical consequence for **your client**: treat "verify used to work, now 404" as *license lapsed / device not activated* and handle it gracefully (fall back to unlicensed behavior, prompt re-activation), rather than as a hard error.

## Your responsibilities

### Run in production mode

`ENVIRONMENT=production` is the single most important setting. In `development` mode Keygate:

- Enables **dev-login** (`POST /auth/dev-login`) — log in as *any* email, no OTP, and if that email is in `ADMIN_EMAILS`, as admin.
- Omits the `Secure` flag on session cookies.
- Loosens CORS and runs Gin in debug mode.

Dev-login has a second guard (404s unless `BASE_URL` contains `localhost`), but don't rely on it — set `ENVIRONMENT=production`. Verify with:

```bash
curl -X POST https://<base-url>/api/v1/auth/dev-login -H "Content-Type: application/json" -d '{"email":"test@test.com"}'
# want: 404, not a session
```

### Close open signup

By default anyone can request an OTP for any email (self-signup). Set `OTP_REQUIRE_EXISTING_USER=true` so codes only go to existing accounts (admins bypass). Unknown emails still get an identical "sent" response, so the endpoint can't be used to enumerate accounts.

### Protect secrets

- Keep `.env` `chmod 600`, never in git.
- **Back up `LICENSE_SIGNING_KEY`** off-server — losing it invalidates every issued token.
- Treat API keys (`kg_live_…`) like passwords; rotate any that leak (mint a new one, update consumers, revoke the old).
- Rotate `JWT_SECRET` if you suspect exposure — it invalidates all sessions (everyone re-logs-in).

### API keys, not embedded secrets

Client apps authenticate with the **license key** via the [public SDK](../integration/sdk.md). Only your servers hold `kg_live_…` API keys. Never embed an admin/`licenses:write` key in a distributed binary — it can mint licenses and will be extracted.

## Attribution (AGPL)

Keygate is AGPL v3 with a Section 7(b) attribution term. The **"Powered by Keygate"** notice in the UI and email footer must be preserved unless you hold a commercial license. Removing it requires purchasing one — see the project NOTICE.
