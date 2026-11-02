# Architecture

WagenPulse is a modular monolith (ADR-0001): one process, one SQLite database (ADR-0002), an embedded React UI and
background workers.

## Process topology

```
                    ┌──────────────────────── wagenpulse serve ────────────────────────┐
 Browser ── https ─▶│ HTTP: /api/v1 (huma) · /auth/* · embedded UI        mgmt: /metrics │
 Terraform ─ bearer▶│                                                     /healthz /readyz│
                    │  services: vehicles · sources · imports · signals · rules · events  │
                    │            routes · adapters · users · tokens · oidc · settings     │
                    │                                                                     │
                    │  scheduler ─▶ jobs (leased, durable) ─▶ collector ─▶ VW portal      │
                    │                                    └──▶ ingest ─▶ observations      │
                    │                                              └──▶ rules ─▶ outbox   │
                    │  dispatchers (one per adapter) ─────────────────────────▶ NATS/Pulse│
                    │                                                                     │
                    │  store: writer (1 conn, BEGIN IMMEDIATE) + readers (query_only)     │
                    └──────────────── data_dir: wagenpulse.db · blobs/ · lock ────────────┘
```

Only one process may own a data directory (file lock). The scheduler, job workers and dispatchers run inside it.
Multi-instance deployment is unsupported.

## Packages

| Package | Owns |
|---|---|
| `cmd/wagenpulse` | CLI: serve, migrate, config check, healthcheck, version, openapi, admin setup-token, keys, backup, restore, dictionary, import |
| `internal/app` | wiring, lifecycle, graceful shutdown |
| `internal/config` | bootstrap configuration (ADR-0004) |
| `internal/store` | SQLite pools, transactions, migrations, snapshots, sqlc queries |
| `internal/keyring`, `internal/secretref` | envelope encryption, external secret references |
| `internal/audit` | hash-chained audit log |
| `internal/auth/...` | users, local login, sessions, CSRF, throttling, TOTP, OIDC, API tokens, RBAC |
| `internal/vehicles` | vehicles, archive, purge |
| `internal/sources` | source configuration, capability/authorization state |
| `internal/providers/vwdataact` | unofficial portal client (acquisition only) |
| `internal/ingest` | archive safety, format detection, parsing, normalization, import orchestration |
| `internal/dictionary` | bundled and imported dictionary versions, signal metadata, unit registry |
| `internal/telemetry` | observation writes, latest state, rollups, history queries |
| `internal/jobs` | durable job queue, scheduler |
| `internal/rules` | rule revisions, evaluation, state, preview, stale sweeps |
| `internal/events` | event envelope, routing, deliveries, replay |
| `internal/adapters`, `internal/adapters/pulse` | adapter registry, dispatcher, Pulse/NATS adapter |
| `internal/retention`, `internal/backup` | retention jobs, backup/restore |
| `internal/api` | REST API (huma), problem documents, idempotency, auth middleware |
| `internal/webui` | embedded UI with CSP nonce |
| `internal/obs/...` | logging, redaction, metrics, health |

## Data flow

1. **Acquisition** (collector or upload) stores the raw bytes content-addressed and records a `raw_deliveries` row.
   Nothing is parsed yet. Duplicate content is detected by SHA-256.
2. **Import job** opens the stored delivery with archive limits, detects the format, parses streaming JSON and
   normalizes values with dictionary metadata.
3. **Write transaction(s)**: observations (dedupe), latest state (precedence), rollups, rule evaluation, events and
   per-adapter deliveries (outbox).
4. **Dispatchers** lease due deliveries per adapter, publish, record attempts, back off and dead-letter.

## Live updates

Polling (ADR-0020).
