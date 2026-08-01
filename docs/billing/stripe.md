# Stripe Integration

Keygate integrates Stripe end-to-end: a customer pays and a license is created (or renewed) automatically, with **three-layer reliability** — the webhook, success-page verification, and a periodic sync — so no payment is ever missed.

## Setup

Always start in Stripe **test mode**.

1. **Create Products/Prices** in Stripe for your paid plans. Recurring Prices for subscriptions, one-time Prices for perpetual/renewal. Copy each `price_…` id.
2. **Set the secret key**: `STRIPE_SECRET_KEY=sk_test_…` in `.env`, then recreate the container (`docker compose up -d`).
3. **Webhook auto-registers.** With `STRIPE_SECRET_KEY` set, `STRIPE_WEBHOOK_SECRET` empty, and `BASE_URL` public, Keygate creates the Stripe webhook endpoint at `{BASE_URL}/api/v1/webhook/stripe` automatically and stores the signing secret. Confirm in the logs.
      - *Manual alternative:* create the endpoint in the Stripe dashboard yourself and set `STRIPE_WEBHOOK_SECRET=whsec_…`.
4. **Link plans to prices.** In the admin dashboard, set each paid plan's `stripe_price_id` to the matching Stripe `price_…`.

### Livemode safety

`STRIPE_LIVEMODE` is auto-derived from the key prefix. Every inbound webhook event whose `livemode` flag doesn't match the server's configured mode is rejected — so a leaked test secret can't replay forged events into production.

## Checkout

Each plan has a `checkout_id`; the hosted checkout URL is:

```text
https://<your-base-url>/pay/<checkout_id>
```

Point your "Buy" buttons at these. The flow: customer clicks → Keygate creates a Stripe Checkout Session (subscription or one-time, matching the Price type) → customer pays → the webhook fulfills.

Return URLs: success goes to `{BASE_URL}/checkout/success`, cancel to `{BASE_URL}/pricing`.

## What the webhook does

On `checkout.session.completed` (and related events), Keygate:

- **Creates the license** on the purchased plan, emails the key to the customer, and records the Stripe customer/subscription ids.
- For subscriptions: advances `valid_until` on `invoice.paid`, moves to `past_due` on `invoice.payment_failed` (starting the dunning ladder), and handles cancellations/refunds/disputes.

All fulfillment is idempotent (keyed on the Stripe session/event id), so retried or duplicated events never double-create.

## Testing

Use Stripe's test card `4242 4242 4242 4242` (any future expiry/CVC). After paying, confirm the new license appears in the dashboard and the key arrives by email.

## Going live

Stripe test and live are separate environments:

1. Recreate the Products/Prices in **live** mode → new `price_…` ids.
2. Swap `STRIPE_SECRET_KEY` to `sk_live_…`, recreate the container.
3. Update each plan's `stripe_price_id` (and `support_renewal_price_id`) to the live ids.
4. Re-test with a real card.

## Support-window renewal

A dedicated flow lets customers extend their [support window](../concepts/support-window.md) without creating a new license:

1. Create a **one-time** Stripe Price for the renewal and set it on the plan's `support_renewal_price_id`.
2. The client calls `POST /license/support/checkout` with `{ "license_key": "KG-…" }` and gets back `{ "checkout_url": "https://…" }`; open it for the customer.
3. On payment, the webhook (routing on a `purpose=support_renewal` metadata flag) extends `support_until` — anchored at `max(now, current support_until)` (early renewers keep their remaining time), by the plan's `support_days` (default 365).
4. Fires a `license.support_renewed` webhook and sends a confirmation email. Idempotent on the Stripe session.

If the plan has no `support_renewal_price_id`, the checkout endpoint returns `503 RENEWAL_NOT_AVAILABLE`. Unknown license keys collapse to `404`.
