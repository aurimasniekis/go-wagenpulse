# WagenPulse under systemd

`wagenpulse.service` runs one WagenPulse instance as a dedicated `wagenpulse` user with a strict sandbox. Exactly one WagenPulse per data
directory (SQLite plus a data-directory lock).

## Layout

| Path | Owner / mode | Purpose |
| --- | --- | --- |
| `/usr/local/bin/wagenpulse` | `root:root 0755` | binary (`make build`, or copy it out of the container image) |
| `/etc/wagenpulse/` | `root:wagenpulse 0750` | configuration directory |
| `/etc/wagenpulse/wagenpulse.yaml` | `root:wagenpulse 0640` | configuration, no secrets |
| `/etc/wagenpulse/kek` | `wagenpulse:wagenpulse 0400` | master key-encryption key; **never** inside `data_dir` |
| `/var/lib/wagenpulse` | `wagenpulse:wagenpulse 0700` | data directory, created by `StateDirectory=wagenpulse` |

## Install

```sh
sudo useradd --system --home-dir /var/lib/wagenpulse --shell /usr/sbin/nologin wagenpulse
sudo install -m 0755 bin/wagenpulse /usr/local/bin/wagenpulse
sudo install -d -m 0750 -o root -g wagenpulse /etc/wagenpulse
sudo install -m 0640 -o root -g wagenpulse wagenpulse.yaml /etc/wagenpulse/wagenpulse.yaml     # see the minimal config below

# KEK: written by the wagenpulse user so it is owned by it (keys init refuses to overwrite an existing file).
sudo -u wagenpulse wagenpulse keys init --out /tmp/wagenpulse.kek
sudo install -m 0400 -o wagenpulse -g wagenpulse /tmp/wagenpulse.kek /etc/wagenpulse/kek && sudo shred -u /tmp/wagenpulse.kek
# Back /etc/wagenpulse/kek up offline, separately from data backups: without it the database is unreadable.

sudo install -m 0644 deploy/systemd/wagenpulse.service /etc/systemd/system/wagenpulse.service
sudo systemctl daemon-reload

sudo systemctl enable --now wagenpulse
wagenpulse healthcheck                       # probes http://127.0.0.1:9464/readyz
systemd-analyze security wagenpulse.service  # exposure score of the sandbox

# Root user: issue a single-use setup token (works while the service runs, ADR-0027).
sudo -u wagenpulse env WAGENPULSE_CONFIG=/etc/wagenpulse/wagenpulse.yaml wagenpulse admin setup-token
```

Open `<public_url>/setup` and paste the token.

Minimal `/etc/wagenpulse/wagenpulse.yaml` (put a TLS-terminating reverse proxy in front, or set `http.tls_cert_file` /
`http.tls_key_file` and bind 443 with the capability lines in the unit):

```yaml
mode: production
data_dir: /var/lib/wagenpulse          # must match StateDirectory=wagenpulse
public_url: https://wagenpulse.example.com
kek:
  file: /etc/wagenpulse/kek
http:
  listen: 127.0.0.1:8080
management:
  listen: 127.0.0.1:9464
proxy:
  trusted_proxies: [127.0.0.1/32, ::1/128]   # the reverse proxy on this host
```

Run host-side commands as the `wagenpulse` user with `WAGENPULSE_CONFIG=/etc/wagenpulse/wagenpulse.yaml`:

- These work while the service runs (ADR-0027): `admin setup-token`, `admin reset-password`, `audit verify`,
  `keys status` and `backup create`.
- These need the service stopped, because they take the data-directory lock: `migrate`, `keys rotate|retire` and
  `restore`.

## Sandbox notes

- `ProtectSystem=strict` makes the whole file system read-only except `ReadWritePaths=/var/lib/wagenpulse` (and the
  `StateDirectory`). Backups written by `backup create --out` need a writable target outside the data directory: run
  the command outside the unit (as above), or add that path to `ReadWritePaths=` for a timer-driven backup service.
- `CapabilityBoundingSet=` is empty: ports 8080 and 9464 need no capabilities.
- `SystemCallFilter=@system-service` minus the privileged groups, `MemoryDenyWriteExecute=yes` (WagenPulse is a static Go
  binary; no JIT), `RestrictAddressFamilies` limited to unix/inet/netlink.

## LoadCredential (alternative)

Instead of a `wagenpulse`-owned `/etc/wagenpulse/kek`, systemd can hand the KEK to the service as a credential. For a non-root
service, systemd places credentials in a private directory as root-owned files with a POSIX ACL for the service user;
the ACL mask shows as group bits (`0440`). WagenPulse accepts that mode **only** for `kek.source: credential`, which reads
`$CREDENTIALS_DIRECTORY/<kek.file>`; world-readable or group-writable files are still refused.

```ini
[Service]
LoadCredential=wagenpulse-kek:/etc/credstore/wagenpulse.kek   # root:root 0400 on disk
```

```yaml
kek:
  source: credential
  file: wagenpulse-kek          # the credential name, not a path
```

Remove `AssertPathExists=/etc/wagenpulse/kek` from the unit when you switch. Unit-tested in WagenPulse (`TestKEKFromSystemdCredential`:
0440 accepted in the credentials directory, refused elsewhere, 0444 refused). Not verified under a real systemd with ACL
support (the test environment's kernel had no tmpfs ACLs).

## Verification status

This unit is adapted from Porch's systemd unit. Porch's unit was verified under systemd 257. **This WagenPulse
copy has not yet been run under a real systemd.** Only the KEK-credential mode handling is unit-tested
(`TestKEKFromSystemdCredential`).

Before production use:

1. Start the service and confirm `systemctl status wagenpulse` shows it active.
2. Run `wagenpulse healthcheck`.
3. Run `systemd-analyze security wagenpulse.service`.
