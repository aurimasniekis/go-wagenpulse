# Deployment files

| File | Purpose |
|---|---|
| [compose.yaml](compose.yaml) | Docker Compose (production-style): hardened container, KEK as a compose secret, data volume |
| [.env.example](.env.example) | Compose interpolation variables (`WP_*`; never `WAGENPULSE_*`, which the binary would reject) |
| [wagenpulse.container.yaml](wagenpulse.container.yaml) | Configuration baked into the image as `/etc/wagenpulse/wagenpulse.yaml` |
| [systemd/](systemd/README.md) | systemd unit with a strict sandbox (not yet verified under a real systemd) |

## Container image layout

| Path | Owner / mode | Purpose |
|---|---|---|
| `/usr/local/bin/wagenpulse` | `root 0555` | Static binary |
| `/etc/wagenpulse/wagenpulse.yaml` | `root 0444` | Default configuration (`WAGENPULSE_CONFIG`); mount your own read-only over it |
| `/var/lib/wagenpulse` | `65532:65532 0700` | Data volume (SQLite, raw deliveries, pre-migration snapshots) |
| `/run/secrets/wagenpulse_kek` | `65532 0400` | KEK, mounted as a secret; never inside the data volume |
| `/var/lib/wagenpulse-kek` | `65532:65532 0700` | Optional mount point for `keys init --out` into a named volume |

The image is built on distroless static (Debian 13, digest-pinned) and has:

- user 65532, no shell, and port 8080/tcp;
- `HEALTHCHECK` running `wagenpulse healthcheck` against the management listener on container loopback.

`scripts/container-smoke.sh IMAGE` checks all of the above, plus the production configuration on a read-only root
filesystem, the KEK refusals, readiness and a graceful SIGTERM exit. See [../docs/operator/install.md](../docs/operator/install.md).
