# WagenPulse

WagenPulse is a self-hosted service for your own Volkswagen Group vehicle data under the EU Data Act. It:

- imports the data deliveries that the VW Group EU Data Act portal provides;
- stores every value as a typed observation with provenance;
- shows fleet state, history charts and data freshness;
- turns observations into rule-driven events and delivers them durably to Pulse (NATS JetStream).

It is a single Go binary with an embedded React UI and a SQLite database. It supports local accounts, OpenID Connect
single sign-on and scoped API tokens, and a Terraform provider manages its configuration.

> **Status: pre-release, not externally reviewed.** The tested and untested parts are listed under
> [What is verified](#what-is-verified). The portal collector uses an **unofficial** interface (see
> [docs/CAPABILITIES.md](docs/CAPABILITIES.md)) and has not been run against the live portal.

## Features

- **Import**
  - Upload the ZIP or JSON deliveries you download from the portal.
  - Imports are content-addressed: identical files are recognised as duplicates whatever their name. The importer
    resists zip bombs and path traversal.
  - Files in an unknown format are kept and can be reprocessed later.
  - The bundled EU Data Act dictionaries describe signals: 1,141 continuous and 5,140 historical keys. Keys outside
    the dictionary are stored as received.
- **Collect** (optional; unofficial portal interface)
  - Scheduled collection with either a browser session cookie or a scripted login.
  - Rate limiting and per-source authorization states (`reauthorization_required`, `no_active_request`,
    `rate_limited`, …).
  - Never bypasses CAPTCHA, consent or terms prompts.
- **Explore**
  - Fleet overview and per-vehicle latest state with freshness.
  - History charts, bucketed with min–max bands; gaps stay gaps.
  - A data explorer across vehicles and signals, with CSV export.
- **Events**
  - Rule types: threshold with hysteresis, change with deadband, transition, and stale data.
  - Debounce and cooldown, immutable rule revisions, and a preview against stored history.
  - Routes send events to adapters. Delivery to Pulse/NATS is at-least-once through a transactional outbox, with
    per-adapter workers, retries, dead letters, manual retry and controlled replay.
- **Access**
  - Roles: root, admin, operator and viewer, enforced server-side for every API operation.
  - Argon2id passwords with throttling, optional TOTP, OIDC (PKCE, nonce, explicit linking), and API tokens.
    Token permissions are the intersection of the owner's role and the token's scopes.
- **Operations**
  - Envelope encryption of stored secrets (KEK outside the data directory) and a hash-chained audit log.
  - Retention, online backups with verification, and restore.
  - Prometheus metrics, health endpoints, a distroless container and a hardened systemd unit.

## Quick start (development)

```sh
make build                                  # web UI + bin/wagenpulse (needs Go 1.27.4, Node 24, pnpm 10)
make dev                                    # development mode: http://localhost:58080, data in .wagenpulse/
make dev-setup-token                        # in another terminal (works while the server runs)
```

1. Open <http://localhost:58080/setup>, paste the token and create the root user.
2. Add a vehicle, then upload a delivery on **Imports**.

Development mode generates a KEK inside the data directory and allows `http://localhost`. Production mode refuses
both. Host-side commands need the same settings as the dev server. Use `make dev-cli ARGS="…"`, for example
`make dev-cli ARGS="backup create --out /tmp/wp-backup"`.

## Production

- **Container:** see [deploy/compose.yaml](deploy/compose.yaml). It has a read-only root filesystem, no
  capabilities, a non-root user, and the KEK as a separate secret.
- **systemd:** see [deploy/systemd/README.md](deploy/systemd/README.md).
- **Operator guides:** [docs/operator/](docs/operator/README.md) covers installation, configuration, the reverse
  proxy, collection, backup and restore, keys, upgrades and troubleshooting.

Put WagenPulse behind a TLS-terminating reverse proxy and set `public_url` to the HTTPS origin users see.

## Terraform

The provider lives in a separate repository, `terraform-provider-wagenpulse` (`registry.terraform.io/aurimasniekis/wagenpulse`).
It manages:

- vehicles, sources, rules, Pulse adapters and routes;
- retention and system settings;
- OIDC providers, users and API tokens.

Telemetry, events and other runtime data are deliberately out of scope; see
[docs/TERRAFORM_COVERAGE.md](docs/TERRAFORM_COVERAGE.md). The provider is not published to the registry.

## What is verified

| Area | Evidence |
|---|---|
| Delivery format (`vwdataact.continuous.v1`) | Real portal deliveries, run locally only and never committed (`TestRealSamples`) |
| Import, dedupe, normalization, latest state, rollups | Go unit and integration tests; rollup buckets equal raw aggregates |
| Rules, events, outbox, Pulse delivery | Integration tests against an embedded NATS server in Pulse operator mode: delivery, missing grant, outage, replay, crash mid-delivery |
| Crash recovery | A real server SIGKILLed mid-import resumes and stores every observation exactly once |
| Auth, RBAC, tokens, OIDC | Unit tests; an OIDC test IdP; a check that every API operation declares a permission |
| UI | 52 Playwright tests in light and dark themes with axe (WCAG 2.1 AA) on every page and the main flows |
| Container | `scripts/container-smoke.sh`: production config, read-only root filesystem, KEK checks, health, graceful stop |
| Performance | `make bench` on a synthetic 1.7M-observation year ([docs/STORAGE.md](docs/STORAGE.md)) |
| **Portal collector** | **Unverified against the live portal.** Covered only by tests against a fake portal that follows the community clients ([docs/CAPABILITIES.md](docs/CAPABILITIES.md)) |
| Historical export format | **Unverified**: no sample yet; such files are stored as `unrecognized` |

## Development

`make help` lists the targets. The main ones:

- `make check`: everything CI runs.
- `make e2e`: Playwright with axe against a throwaway instance.
- `make bench`: the storage benchmarks.

Read [AGENTS.md](AGENTS.md), [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/DECISIONS.md](docs/DECISIONS.md) before
contributing.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

The bundled dictionaries are derived from the EU Data Act data lists that Volkswagen AG publishes. They are included
so that signals can be described, and they remain Volkswagen AG's documents.

WagenPulse is not affiliated with or endorsed by Volkswagen AG or any Volkswagen Group brand.
