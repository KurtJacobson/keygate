# Maintenance & Updates

## Backups

The PostgreSQL database is the only unrecoverable state on the box (the binary and config you can rebuild; the data you can't). Schedule a **nightly dump shipped off-server**.

```bash
# /etc/cron.daily/keygate-backup  (chmod +x)
#!/bin/bash
docker compose -f /opt/keygate/docker-compose.yml exec -T db \
  pg_dump -U keygate keygate | gzip > /root/keygate-$(date +%F).sql.gz
# then copy off-box, e.g.:
# rclone copy /root/keygate-$(date +%F).sql.gz b2:my-backups/keygate/
```

Adjust the container/user/db names to your compose file. Test a restore at least once. Also keep a copy of `.env` (especially `LICENSE_SIGNING_KEY`) somewhere safe.

## Restarting

```bash
cd /opt/keygate
docker compose restart          # restart with current config
docker compose up -d            # recreate — required to pick up .env changes
docker compose logs -f          # watch (Ctrl+C to stop watching)
docker compose ps               # both containers up / postgres healthy
```

Environment variables are baked in at container creation, so a plain `restart` won't apply `.env` edits — use `up -d`.

## Updating Keygate

Keygate images are published to GHCR. The in-app dashboard shows an **update-available** badge when a newer GitHub Release exists on the repository.

### The release model

- **Docker tags** — pushing to `main` builds `:latest`; pushing a version tag `vX.Y.Z` builds `:X.Y.Z` **and** `:latest` and cuts a GitHub Release.
- **The update badge** compares against GitHub **Releases** (tags), not branches. So it only lights up once you tag a versioned release — pushing `main` alone updates `:latest` but creates no Release.

### Applying an update

```bash
cd /opt/keygate
docker compose pull        # fetch the new image
docker compose up -d       # recreate with it
docker compose logs -f     # watch migrations apply on startup
```

Migrations run automatically and are idempotent (advisory-locked, checksum-verified). Because the database persists in its volume, your data survives the image swap.

!!! tip "Pin a version in production"
    `:latest` is convenient but moves under you. For predictable production updates, pin `image: ghcr.io/kurtjacobson/keygate:X.Y.Z` in `docker-compose.yml` and bump it deliberately.

## Health checks

```bash
curl -I https://<base-url>/            # HTTP reachable
docker compose ps                       # containers healthy
docker stats --no-stream                # RAM/CPU per container
journalctl -u caddy                     # proxy/TLS issues
```

If the box has been under memory pressure, `free -h` and `docker stats` tell you whether Postgres is swapping heavily — the signal to move up a VPS tier.

## Housekeeping

Keygate self-prunes transient rows (OTP codes, revoked refresh tokens, expired activations) on an hourly loop, so tables don't grow unbounded. No manual cleanup is needed.
