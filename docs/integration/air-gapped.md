# Air-gapped Licensing

Some machines never touch the internet — ever. They can't call `activate` or `verify`, so the normal online flow doesn't apply. For these, an admin issues a **perpetual, machine-bound license file** out-of-band; the device verifies it entirely offline, forever.

This builds on the same signed token described in [Offline Token Verification](offline-tokens.md) — read that first for the crypto and payload details. The difference here is **how the token is issued**: not by the device calling in, but by an admin minting a long-lived file you deliver by email or USB.

## The flow

```text
  air-gapped machine          you / admin                   Keygate
 ───────────────────         ─────────────                 ─────────
  1. show machine code  ──▶  2. receive it (phone/email)
                             3. issue offline token   ──▶   sign (Ed25519)
                        ◀──  4. deliver .lic file      ◀──  return token
  5. drop in license dir
  6. verify offline ✓ forever
```

1. On the air-gapped machine, your app displays a **machine code** — a stable hardware identifier (see [The machine code](#the-machine-code) below).
2. The customer reads it to you **from another device** (phone, email — not the air-gapped box).
3. You issue the license in Keygate and generate a signed token bound to that machine code.
4. You send them the resulting `.lic` file.
5. They drop it into the app's license folder.
6. The app verifies it locally against the embedded public key — no network, indefinitely.

## Issuing a token

### From the admin dashboard

Open a license → **Offline license**. Enter the customer's machine code, leave **Expiry** empty for a perpetual file (or set a date to force re-issuing on renewal), and click **Generate**. Copy the token or **Download `.lic`** and send it to the customer.

The button appears only for `active` / `trialing` licenses.

### From the API

```http
POST /api/v1/admin/licenses/{id}/offline-token
Authorization: Bearer kg_live_…
Content-Type: application/json

{ "identifier": "<machine-code>", "expires_at": "" }
```

- `identifier` — the device's machine code (required).
- `expires_at` — RFC 3339 timestamp, or empty/omitted for a **perpetual** token (the default for air-gapped installs).

Response:

```json
{
  "token": "<base64url payload>.<base64url signature>",
  "fingerprint": "a1b2c3d4e5f6a7b8",
  "perpetual": true,
  "expires_at": ""
}
```

Deliver the `token` string to the customer as a file (e.g. `yourproduct.lic`). The `fingerprint` is included for support reference — it also lives inside the token as `fpr`.

## The machine code

The token is bound to the device by `fpr = SHA256(identifier : product_id)`. For this to work:

- Your app computes a **stable hardware identifier** (e.g. a hash of volume serial + CPU/board ID) and displays it as the machine code the customer reads to you.
- At verify time the app recomputes the **same** identifier, derives `SHA256(identifier : product_id)`, and compares it to the token's `fpr`. A mismatch means the file was issued for a different machine — reject it.

Pin the identifier algorithm down once and freeze it: if the value the customer reads to you differs from what the app recomputes later, every license breaks. This is the whole anti-copy mechanism — it's enforced client-side, since there's no server to check against.

## Verifying on the device

Identical to the online case — see [Offline Token Verification → Client verification steps](offline-tokens.md#client-verification-steps). In short: embed the public key from `GET /license/pubkey` at build time, check the Ed25519 signature, confirm `fpr` matches this machine, and read `ftr` for feature gating. A perpetual token carries `exp = 0`, which `Verify` treats as never-expiring, so there is no clock check to satisfy.

## Trade-offs to plan for

Offline licensing gives up things the online flow provides. These are inherent, not Keygate limitations:

- **No revocation.** Once a perpetual token is on a never-online machine, it's valid forever — there's no kill switch. If you need to be able to cut a device off, don't issue perpetual: set an `expires_at` and re-issue on renewal.
- **Clock rollback** defeats a bounded `expires_at` (the user can set the system clock back). It does **not** affect perpetual tokens, since there's no expiry to bypass. For bounded tokens, optionally have the client persist a monotonic "last seen" timestamp and refuse if the clock jumps backward.
- **`max_activations` isn't enforced** offline — there's no server to count against. Control it by issuing exactly one file per machine code.

## Support renewal for air-gapped installs

Perpetual + paid support works here too, mapping cleanly onto [perpetual fallback](../concepts/support-window.md): the file the customer holds keeps the software running forever, while the support window (`sup`) gates newer releases. To renew, issue a **new** offline token with a later `expires_at` / refreshed support window and send the updated `.lic` file — the sneakernet equivalent of a support renewal.
