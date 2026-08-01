# Software Updates

Keygate distributes signed application updates: you upload per-platform artifacts grouped into a release, and clients consume standard update feeds or fetch license-gated download URLs. This requires [object storage](../getting-started/configuration.md#object-storage-optional-software-distribution-only) to be configured.

## How releases work

- A **release** is a version (e.g. `1.2.0`) in a channel (`stable`, `beta`, `alpha`, `dev`) with one or more per-platform **artifacts** (darwin-arm64, windows-x64, linux-x64, …).
- **Atomic publish gate** — a release only becomes visible once fully uploaded; half-uploaded releases never leak.
- **Yank** — instantly withdraw a bad release.
- **Ed25519 signing** — each product has its own signing key (private key encrypted at rest under AES-256-GCM with an HKDF-derived subkey). Trust is established by the artifact signature, not URL secrecy.
- **Server-side SHA-256** — integrity hashes are computed server-side; the client's hash is never trusted.

Uploads use presigned URLs (direct browser→storage, no proxying through Keygate).

## Update feeds (public)

One publish serves every major updater format — all public, since trust comes from the signature, not secrecy:

| Feed | Format | Endpoint |
|------|--------|----------|
| Sparkle (macOS) | appcast XML | `GET /api/v1/releases/{product_slug}/feed.xml` |
| Velopack (Windows) | JSON feed | `GET /api/v1/releases/{product_slug}/feed.json` |
| Tauri (cross-platform) | manifest | `GET /api/v1/releases/{product_slug}/upgrade.json` |

Feeds are per-platform and honor the channel fallback chain (a beta user also sees stable). The product's `minimum_supported_version` is embedded so clients can force-upgrade off builds below the floor.

!!! note "Feeds are public by design"
    Genuinely private builds don't belong in these feeds — put them on internal CI/CD distribution (a private bucket, TestFlight, etc.). Because license gating happens at app start via `verify`, a public update feed never breaks an installed client when a license rotates.

## License-gated downloads

For manual or gated downloads, `POST /license/download` returns a short-lived presigned URL:

```json
{ "license_key": "KG-XXXX-…", "platform": "windows-x64", "version": "1.2.0", "channel": "stable" }
```

- Omit `version` to get the latest published release for the channel/platform.
- Returns `url`, `expires_at`, `version`, `platform`, `sha256`, `file_size`.

### Support-window enforcement (perpetual fallback)

When the license has a [`support_until`](../concepts/support-window.md), downloads honor it:

- Any release **published before** `support_until` is always downloadable — forever.
- "Latest" resolves to the newest release the support window covers.
- A pinned version newer than the window returns **`403 SUPPORT_EXPIRED`**.
- No `support_until` = unlimited.

**Failure modes:** `400` invalid platform/channel · `403` license inactive or `SUPPORT_EXPIRED` · `404` `RELEASE_NOT_FOUND` · `410 RELEASE_YANKED` · `503 STORAGE_DISABLED`.

## Object storage

S3-compatible — Cloudflare R2, AWS S3, MinIO, anything speaking SigV4. Configure via the `STORAGE_*` variables and the `RELEASE_KEY_ENCRYPTION_KEY` master key (which also encrypts license keys at rest). See [Configuration](../getting-started/configuration.md#object-storage-optional-software-distribution-only).
