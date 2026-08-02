# Offline Token Verification

`activate` and `verify` return a **signed token** your app can verify locally, with no network call, during its validity window. This is what lets a licensed app keep working offline and survive brief server outages.

## Why signed tokens (and not HMAC)

Tokens are signed with **Ed25519**. The server holds the private key; clients fetch the matching public key once from `GET /license/pubkey` and verify locally.

Keygate deliberately does **not** use HMAC: HMAC is symmetric, so any client that can verify can also forge. Shipping a shared secret inside a desktop binary makes it extractable, letting an attacker mint arbitrary tokens. Ed25519 keeps the signing capability on the server only.

## Token format

```text
base64url(payload) . base64url(signature)
```

The signed bytes are the **base64url-encoded payload string** (not the raw JSON). To verify: split on the last `.`, base64url-decode the signature, and run `ed25519.Verify(pubkey, []byte(payloadB64), sig)`.

## Payload fields

| Field | Meaning |
|-------|---------|
| `lid` | License ID |
| `pid` | Product ID |
| `pln` | Plan ID |
| `sts` | Status (`active`, `trialing`, …) |
| `did` | The activated identifier (device/user) |
| `ftr` | Features map (entitlements) |
| `iat` | Issued-at (unix seconds) |
| `exp` | Token expiry (unix seconds) — online tokens are short-lived (~7 days); `0` = never expires (see [Air-gapped Licensing](air-gapped.md)) |
| `grc` | Grace days for the plan |
| `sup` | Support-window end (unix seconds; `0`/absent = unlimited) |
| `nce` | Per-issuance nonce (replay protection) |
| `fpr` | Fingerprint = `SHA256(identifier + ":" + product_id)`, truncated — binds the token to this device |

## Client verification steps

1. **Fetch the public key once** — `GET /license/pubkey`, cache it (it's stable; rotating it invalidates all tokens).
2. **On each start**, `verify` online if you can and cache the returned token.
3. **Offline**, verify the cached token:
      - Check the Ed25519 signature against the cached public key.
      - Check `exp` is in the future (reject stale tokens).
      - Optionally check `fpr` matches this device, so a token leaked from one machine can't be replayed on a sibling that knows the key.
4. **Grace window** — allow the app to run on a cached-but-expiring token for a grace period when the server is unreachable, so a network blip doesn't lock out a paying customer.

## Reading the support window offline

`sup` lets you show renewal UX without a round trip: if `sup > 0` and `now > sup`, the support window has lapsed (the license still works — only updates are gated). Prompt the user to renew via [support checkout](../billing/stripe.md#support-window-renewal).

## Language notes

Ed25519 verification is in every language's crypto toolkit — Go (`crypto/ed25519`), .NET (`NSec` or BouncyCastle on .NET Framework), Rust (`ed25519-dalek`), JS (`tweetnacl`/WebCrypto), etc. You only ever need the **public** key on the client.
