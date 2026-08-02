# Licensing Models

A plan's `license_type` and `license_model`, combined with the support window, express five commercial models. This page explains each and how a license behaves under it.

## The status lifecycle

Every license has a `status`:

| Status | Usable? | Meaning |
|--------|:---:|---------|
| `active` | ✅ | Normal, in-force license. |
| `trialing` | ✅ | Inside a trial window. |
| `past_due` | ✅ (grace) | Payment failed; still usable during grace/dunning. |
| `canceled` | until period end | Subscription canceled; usable until `valid_until`. |
| `expired` | ❌ | Past `valid_until` + grace. |
| `suspended` | ❌ | Manually paused by an admin. |
| `revoked` | ❌ | Permanently killed. |

`verify` (and other SDK checks) collapse every unusable/unknown case into a single `404 LICENSE_NOT_FOUND` — see [Security](../self-hosting/security.md#oracle-hardening).

## 1. Subscription

`license_type: subscription`. Recurring billing via Stripe. The webhook advances `valid_until` each period; a failed payment moves the license to `past_due` and starts the dunning email ladder. After the grace period the license expires.

- Set `stripe_price_id` to a **recurring** Stripe Price.
- `grace_days` controls how long a `past_due` license keeps working.

## 2. Perpetual

`license_type: perpetual`. A one-time purchase; the license never expires (`valid_until` stays empty). The customer owns this version forever.

- Set `stripe_price_id` to a **one-time** Stripe Price (or issue manually).
- No renewal, no dunning.

## 3. Trial

`license_type: trial` with `trial_days > 0`. On creation the license gets `status: trialing` and `valid_until = now + trial_days`. When the window passes, the hourly checker flips it to `expired` and sends the trial-expired email.

- Trials can be issued directly (admin API) or via a Stripe checkout on a trial plan.
- An expired trial fails `verify` (404), so your client falls back to its unlicensed behavior.

!!! note "Trials don't auto-downgrade"
    When a trial expires it becomes `expired`, not "reverted to free." If your model is free-with-trial, keep the free tier as a **separate** license the client falls back to, rather than expecting the trial license to become a free one.

## 4. Floating (concurrent)

`license_model: floating` (on a saas/hybrid product). Instead of node-locking to devices, a floating license allows up to `max_activations` **concurrent** sessions. Clients check out a session, send heartbeats, and check in when done; idle sessions time out after `floating_timeout` minutes and free the slot.

Endpoints: `POST /license/floating/checkout`, `/floating/heartbeat`, `/floating/checkin`.

## 5. Perpetual + paid support

A perpetual license (never expires) plus a separate, time-boxed **support window** that gates updates — the JetBrains model, also known as **perpetual fallback**. The license keeps working forever, but access to *new releases* stops when support lapses; the customer keeps every version published while their support was active (the "fallback") and renews to receive newer ones.

This is powerful enough to have its own page: **[The Support Window](support-window.md)**.

## Choosing a model

| You want… | Use |
|-----------|-----|
| Recurring revenue, feature gating | Subscription |
| Sell-once, own-forever | Perpetual |
| Time-limited evaluation | Trial |
| Shared seats across a team/lab | Floating |
| Sell-once but charge for updates/support | Perpetual + paid support |

These aren't mutually exclusive across your catalog — different plans on the same product can use different models.
