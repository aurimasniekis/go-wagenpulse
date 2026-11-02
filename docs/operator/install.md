# Installing WagenPulse

WagenPulse is one process with one SQLite database. Run exactly one instance per data directory; it holds a lock on
that directory. Multiple replicas are not supported.

You need three things:

- **A data directory**: local disk only, mode 0700. Network filesystems are refused.
- **A KEK** (key-encryption key): a separate file outside the data directory, mode 0400, owned by the service user.
  - It encrypts the stored credentials: portal secrets, Pulse `.creds`, OIDC client secrets and TOTP secrets.
  - **Back it up offline.** Without it the stored secrets cannot be decrypted.
- **A public URL**: the HTTPS origin users reach WagenPulse at, normally through a TLS-terminating reverse proxy.

## Docker Compose (recommended)

```sh
cd deploy
cp .env.example .env                  # set WP_PUBLIC_URL
install -d -m 0700 secrets && sudo chown 65532:65532 secrets
docker compose --profile init run --rm keygen          # writes secrets/wagenpulse.kek (0400)
docker compose up -d && docker compose ps               # healthy after a few seconds
docker compose exec wagenpulse /usr/local/bin/wagenpulse admin setup-token
```

Open `WP_PUBLIC_URL/setup`, paste the token (valid for 30 minutes, single use) and create the root user.

The container image has:

- user 65532, no shell, and a read-only root filesystem with all capabilities dropped;
- the data volume at `/var/lib/wagenpulse`;
- the KEK at `/run/secrets/wagenpulse_kek`;
- the public HTTP port 8080;
- metrics and health on container loopback (`127.0.0.1:9464`).

The image's `HEALTHCHECK` runs `wagenpulse healthcheck`.

## systemd

See [../../deploy/systemd/README.md](../../deploy/systemd/README.md) for the unit, the file layout and the minimal
configuration.

## Plain binary

Release archives contain:

- the static binary;
- the container config (a good starting point for `/etc/wagenpulse/wagenpulse.yaml`);
- the systemd unit;
- LICENSE and NOTICE.

Steps:

```sh
wagenpulse keys init --out /etc/wagenpulse/kek
wagenpulse config check --config /etc/wagenpulse/wagenpulse.yaml        # prints the effective configuration
wagenpulse serve --config /etc/wagenpulse/wagenpulse.yaml
```

## After the first start

1. Sign in as root, then go to **Account & security** and enable two-factor authentication.
2. Optionally enable **Settings → Require two-factor for admins**.
3. **Vehicles → Add vehicle** with its VIN. The VIN matches deliveries to the vehicle, and the UI masks it.
4. **Imports**: upload a delivery downloaded from the portal. See [collection.md](collection.md).
5. For Terraform, create an API token under **API tokens** using the *terraform* preset. To let Terraform manage
   tokens as well, add `tokens.delegate`.
6. Schedule backups ([backup-restore.md](backup-restore.md)).
