# Seats & Email

## Team seats

For `saas` and `hybrid` products, a license can host a **team** — the customer manages their own members within the seat limit you set per plan (`max_seats`).

### Roles

| Role | Can |
|------|-----|
| `owner` | Everything, including managing admins. |
| `admin` | Invite/remove members, manage the team. |
| `member` | Use the license as part of the team. |

### Invite flow

1. The license owner (or an admin seat) invites an email from the portal — this consumes a seat, capped atomically at `max_seats`.
2. Keygate emails a one-time invite link (token-based, time-limited).
3. The invitee clicks the link → `POST /invites/accept` (or `/portal/seats/accept`) creates their account **and** logs them in — the mailed token proves email ownership, so no separate OTP round-trip is needed.

Owners/admins can remove members to free seats. Expired or revoked invites free their slot automatically.

!!! note "Seats vs activations"
    **Seats** (per-user, saas/hybrid) and **activations** (per-device, desktop/hybrid) are different limits. A desktop product caps *devices*; a SaaS product caps *team members*. A hybrid product can use both.

## Email

Keygate sends transactional email over SMTP (see [Configuration → Email](../getting-started/configuration.md#email-smtp)). Because admin login is email OTP, working SMTP is effectively required.

### Emails sent

| Template key | Sent when |
|--------------|-----------|
| `license_created` | A license is issued (carries the key). |
| `license_expiring` | 7/3/1 days before `valid_until`. |
| `license_expired` | A license expires. |
| `trial_expired` | A trial ends. |
| `license_suspended` | A license is suspended. |
| `support_expiring` | 30/7 days before `support_until`. |
| `support_ended` | The support window lapses. |
| `support_renewed` | A support renewal is paid. |
| `quota_warning` | Usage crosses the threshold. |
| `seat_invite` | A team invite is sent. |
| `payment_failed` / dunning | Subscription payment fails (ladder of reminders). |

### Customizing templates

Every template is editable in the dashboard under **Email Templates**. Templates are HTML with `{{.Variable}}` placeholders (e.g. `{{.Product}}`, `{{.LicenseKey}}`, `{{.EndsAt}}`, `{{.RenewedUntil}}`). Each template lists its available variables. Resetting a template restores the built-in default.

!!! tip "SMTP port"
    Many VPS providers block outbound port 25. Use `SMTP_PORT=587` (STARTTLS) with a transactional provider (Brevo, Mailgun, SES, Postmark). A real sending domain also improves deliverability and OTP inbox placement.
