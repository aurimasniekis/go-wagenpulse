# Configuration

Configuration comes from a YAML file (`--config`, or `WAGENPULSE_CONFIG`). Single keys can be overridden with
`WAGENPULSE_<SECTION>_<KEY>` environment variables. `wagenpulse config env` lists every override, and
`wagenpulse config check` validates the configuration and prints the effective values.

Unknown keys and unknown `WAGENPULSE_*` variables are **errors**. This catches typos and stops unrelated variables
from changing behaviour.

Runtime settings are stored in the database and edited in **Settings** or with Terraform. They are:

- the sign-in methods;
- session lifetimes, within the configured maximums;
- token lifetimes;
- default event policies;
- the stale threshold;
- retention.

## Required in production

| Key | Meaning |
|---|---|
| `public_url` | HTTPS origin users reach WagenPulse at. It is used for the CSRF/Origin check, cookies (`__Host-` over HTTPS) and the OIDC redirect URI |
| `data_dir` | Local data directory (default `/var/lib/wagenpulse`) |
| `kek.file` | KEK path, outside `data_dir`, mode 0400. Alternatives: `kek.source: credential` (systemd `LoadCredential=`) or `env` (`WAGENPULSE_KEK`, discouraged) |

`mode: development` relaxes these for local work: it allows `http://localhost` and generates a KEK inside the data
directory. **Never run development mode in production.**

## Listeners

- `http.listen` (default `127.0.0.1:8080`): the UI and API.
  - Serve TLS directly with `http.tls_cert_file`/`http.tls_key_file`, or put a reverse proxy in front.
- `management.listen` (default `127.0.0.1:9464`):
  - `/metrics` (Prometheus), `/healthz` and `/readyz`;
  - optional pprof (`management.pprof`).
  - Keep it private.

## Reverse proxy

Terminate TLS at the proxy and forward to `http.listen`, preserving `Host`. Set `proxy.trusted_proxies` to the
proxy's addresses: `X-Forwarded-For` is believed only from those addresses, and it feeds login throttling and the
audit log.

Example (Caddy):

```
wagenpulse.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Uploads can be up to `ingest.max_upload_bytes` (50 MiB by default), so raise the proxy's body limit to match.

## Outbound network policy

WagenPulse connects out to the VW portal (when a source is configured), to NATS adapters and to OIDC providers. Every
connection goes through one dialer with these rules:

- Link-local, cloud-metadata and multicast addresses are always refused.
- Loopback is refused unless listed in `outbound.allow_cidrs`. For a local NATS server, add `127.0.0.1/32`.
- Private (RFC 1918/ULA) ranges are allowed by default (`outbound.allow_private: true`), because a home NATS server
  usually lives on the LAN. Set it to `false`, or use `outbound.deny_cidrs`, to narrow this.
- `outbound.ca_file` adds CA certificates for outbound TLS.

## Secrets

Secrets entered in the UI or API are write-only and are stored encrypted with keys wrapped by the KEK. Instead of
storing them, you can use an external reference:

- `env:WAGENPULSE_SECRET_NAME` reads an environment variable. Only names starting with `WAGENPULSE_SECRET_` are
  allowed.
- `file:/run/secrets/name` reads a file inside one of `secrets.file_dirs` (default `/run/secrets`,
  `/etc/wagenpulse/secrets`).

References are resolved when the secret is used, so rotating the file or variable needs no change in WagenPulse.

## Limits

These bound uploads and archive extraction:

- `ingest.max_upload_bytes` (50 MiB);
- `ingest.max_extracted_bytes` (200 MiB);
- `ingest.max_entries` (1000);
- `ingest.max_ratio` (100:1).

`workers.jobs` sets the number of parallel import and collection jobs. `workers.delivery_per_adapter` sets the number
of concurrent publishes per adapter; it defaults to 1 to keep order.
