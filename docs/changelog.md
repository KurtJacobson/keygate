# Changelog

Notable changes in this fork of Keygate, newest first. Versions before
0.1.2 are inherited from upstream Keygate; the entries below cover what
this fork adds on top.

## 0.1.4 — Offline & air-gapped licensing

**Added**

- **Offline (air-gapped) license issuance.** Admins can mint a perpetual, machine-bound license file (`.lic`) for devices that never connect to the internet — `POST /admin/licenses/:id/offline-token`, plus an "Offline license" action in the admin license view. The token is Ed25519-signed, verified fully offline, and bound to the device via its fingerprint. See [Air-gapped Licensing](integration/air-gapped.md).
- **Self-service offline activation.** From the customer portal, a license owner (or accepted seat) can activate **one** air-gapped machine and download its license file. Switching machines requires an admin to clear the activation — enforced as one offline activation per license (partial unique index). Hidden for SaaS products.
- **Docs:** new *Air-gapped Licensing* integration page, including the machine-code contract and the reconnect / revocation semantics.

**Fixed**

- Postgres 18 container failing to start because the data volume was mounted at `/var/lib/postgresql/data` instead of `/var/lib/postgresql`.

## 0.1.3 — Theming & security hardening

**Added**

- **Dedicated favicon setting** (`favicon_url`) separate from the logo, plus brand-color **tints derived from a single primary color** so admins don't hand-pick a palette.

**Security**

- **Close open OTP signup** with `OTP_REQUIRE_EXISTING_USER` — codes go only to existing accounts (admins bypass); unknown emails get an identical response, so the endpoint can't enumerate accounts.
- **Pin JWT verification to HS256**, removing the algorithm-confusion class.
- **Restrict CORS** to `BASE_URL` (plus localhost outside production); unknown origins are rejected. Closes a credentialed-CORS hole on internet-reachable non-production deployments. See [Security](self-hosting/security.md).
- **Enumeration-safe wording** on the login OTP step.

**Fixed**

- Custom-logo favicon now updates every `<link rel="icon">`, not just the first.

## 0.1.2 — License expiration & paid support (first fork release)

**Added**

- **License expiration (`valid_until`).** Set or clear a fixed expiry per license (admin API + UI); licenses with no expiry show as **Perpetual**. Dates are interpreted as end-of-day in the admin's timezone.
- **Support window (perpetual + paid support).** `licenses.support_until` and `plans.support_days` implement the JetBrains-style "perpetual fallback": the license never expires, but access to newer releases is gated on the paid support window. `support_until` is surfaced in the verify/activate response and the signed offline token (`sup`), with reminder and lapse emails and a `license.support_ended` webhook. See [The Support Window](concepts/support-window.md).
- **Self-serve Stripe support renewal.** `POST /license/support/checkout` extends `support_until` (per-plan `support_renewal_price_id`), fires a `license.support_renewed` webhook, and emails a confirmation.
- **Portal device-activation management** — customers free their own activation slots without a support ticket.

**Changed**

- **Rebranded fork.** Docker images and the in-app update check point at `kurtjacobson/keygate`; upstream sponsorship links removed.

**Fixed**

- SMTP compatibility fixes for Office 365 / Exchange Online (AUTH negotiation and envelope sender).
