# Webhooks

Keygate emits webhooks so your systems can react to license lifecycle events — provisioning downstream access, syncing a CRM, triggering fulfillment, etc.

## Configuring endpoints

In the dashboard under **Webhooks**, add an endpoint with:

- **URL** — where events are POSTed.
- **Events** — which event types to receive.
- **Secret** — used to HMAC-sign each delivery so you can verify authenticity.

Deliveries are retried with backoff; you can inspect delivery history and resend individual deliveries from the dashboard, and send a test event.

## Event types

| Event | Fires when |
|-------|-----------|
| `license.created` | A license is issued (manual or via Stripe). |
| `license.suspended` | A license is suspended. |
| `license.reinstated` | A suspended/expired/canceled license is reinstated. |
| `license.revoked` | A license is revoked. |
| `license.canceled` | A subscription license is canceled. |
| `license.expired` | A license passes `valid_until` + grace (reason: `trial_ended`, `grace_period_ended`, …). |
| `license.support_ended` | A license's [support window](../concepts/support-window.md) lapses. |
| `license.support_renewed` | A support window is extended via paid renewal. |
| `quota.warning` | Usage crosses the warning threshold. |
| `quota.exceeded` | A quota cap is hit. |
| `seat.added` · `seat.removed` | Team seat changes. |
| `plan.changed` | A license moves to another plan. |

## Verifying deliveries

Each delivery is signed with your endpoint secret (HMAC). Verify the signature before trusting the payload — treat the body as untrusted until the signature checks out. Deliveries also carry a stable id you can use for idempotency on your side.

## Payloads

Payloads are JSON with the event type and the relevant ids (`license_id`, `email`, and event-specific fields such as `support_until` or `reason`). Since events can be retried or delivered out of order, make your handlers idempotent and treat each as a signal to re-fetch authoritative state from the admin API when in doubt.

## Stripe vs Keygate webhooks

Don't confuse the two:

- **Stripe → Keygate** (`/api/v1/webhook/stripe`) is inbound — Stripe telling Keygate about payments. See [Stripe Integration](../billing/stripe.md).
- **Keygate → you** (configured here) is outbound — Keygate telling *your* systems about license events.
