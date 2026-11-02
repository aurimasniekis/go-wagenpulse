# Architecture decision records

Each ADR records a decision, its context and its consequences. Superseded ADRs stay in place with a pointer. Any
relaxation of a security property is labelled **security-relevant change**.

## ADR-0001 — Modular monolith, single process, single writer

**Context.** WagenPulse is self-hosted, usually for one household or a small fleet. Data arrives at most every
15 minutes per vehicle.

**Decision.** One Go binary (`wagenpulse serve`) contains the API, the embedded UI, the scheduler, the job workers and
the delivery dispatchers. The process takes an exclusive `flock` on the data directory; a second process refuses to
start. Packages under `internal/` own their tables and expose services; cross-domain calls go through those services.

**Consequences.** No message broker, no separate worker deployment. Horizontal scaling and high availability are not
supported. Scheduler ownership is trivially the one process. The future migration trigger is a requirement for HA or
multiple writers (see ADR-0002).

## ADR-0002 — SQLite as the only database

**Context.** Workload assumptions: 1–20 vehicles; deliveries every ≥15 minutes with 60–1100 values each; most values
repeat their capture timestamp between deliveries and deduplicate; under 50k new observations per vehicle per day
(worst case ~100k); concurrent UI reads while ingesting; a transactional outbox is required.

**Decision.** SQLite via `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`), following HallPass/Porch:
- one writer connection with `BEGIN IMMEDIATE` (`_txlock=immediate`), a bounded BUSY retry, and a reader pool
  (`query_only(1)`, `max(4, GOMAXPROCS)` connections);
- `journal_mode=WAL`, `synchronous=FULL`, `foreign_keys=1`, `busy_timeout=5000`, `trusted_schema=0`,
  `auto_vacuum=INCREMENTAL`; pragmas verified at open;
- `STRICT` tables, Unix-microsecond integer timestamps, `CHECK` constraints for enums;
- goose forward-only migrations with a pre-migration snapshot; sqlc typed queries plus hand-written SQL for dynamic
  filters;
- write transactions are short, never perform network I/O and batch at most `ingest.batch_size` (default 2000)
  observations.

**Rejected.** PostgreSQL/TimescaleDB: operational cost (a second service, backups, upgrades) without a concrete need at
this scale. A time-series database: records having timestamps is not a reason.

**Consequences.** Throughput is bounded by one writer (measured in `docs/STORAGE.md`). Migration triggers: more than
~100 vehicles, sustained writes above ~500 rows/s, a need for HA or multiple writers.

## ADR-0003 — Identifiers

Public identifiers are `<prefix>_<26 lowercase Crockford base32 chars of a UUIDv7>` (HallPass/Porch `internal/ids`).
High-volume telemetry tables reference vehicles and signals by integer surrogate keys (`vehicles.num`,
`signals.num`) to keep rows small; integer keys never leave the API. Signals are publicly identified by their upstream
dictionary key (UUID).

## ADR-0004 — Bootstrap configuration vs runtime configuration

The YAML file / `WAGENPULSE_*` environment holds only deployment and bootstrap settings: data directory, listeners,
public URL, KEK location, process limits. All durable operator configuration (vehicles, sources, rules, adapters,
routes, retention, login methods, OIDC providers, users, tokens) lives in the database and is managed only through the
versioned API (used by the UI and the Terraform provider). There is no second configuration path that could drift.

## ADR-0005 — Upstream acquisition: verified file import plus an opt-in unofficial portal collector

**Context.** See `docs/CAPABILITIES.md`. There is no documented machine API. The portal's web backend is used by
several community clients. The project owner chose to support it explicitly (2026-11-02).

**Decision.**
1. File import of portal deliveries (upload, CLI, API) is the primary, verified path.
2. Source kind `vw_data_act_portal` implements the community-observed flow (session cookie or scripted VW Identity
   login, then metadata → list → download). It is opt-in, labelled "unofficial portal interface" in the API and UI,
   identifies itself with `WagenPulse/<version> (+https://github.com/aurimasniekis/go-wagenpulse)`, performs no priming
   requests, polls no more often than every 15 minutes, honours `Retry-After`, and backs off on 429/5xx.
3. WagenPulse never creates, renews or accepts upstream data requests or consents. States `no_active_request` and
   `reauthorization_required` are surfaced instead.
4. No live-verification claim is made until an operator runs it and the result is recorded in `docs/CAPABILITIES.md`.

**Consequences.** The collector may break when VW changes the portal; failures surface as source states, never as fake
success. The adapted portal client keeps acquisition separate from parsing (the collector only stores raw deliveries).

## ADR-0006 — Bundled derived dictionaries

**Decision.** The two official dictionaries are converted by `tools/dictgen` (Ghostscript text extraction with column
positions) into JSON under `internal/dictionary/bundled/` and embedded in the binary. The project owner accepted the
redistribution risk (2026-11-02); NOTICE attributes the source. Operators can import newer dictionary versions; every
version is stored and signals keep a pointer to the version that last described them.

## ADR-0007 — Encryption of stored secrets

**Decision.** HallPass/Porch keyring: a 32-byte KEK supplied from a file outside the data directory, a systemd
credential or (discouraged) `WAGENPULSE_KEK`; per-purpose data keys (`field`, `token`) wrapped by the KEK; AES-256-GCM
with AAD binding every ciphertext to table, column and row; `wagenpulse keys rotate` re-wraps data keys. Secrets
configured through the API are either sealed inline or external references (`env:NAME`, `file:/abs/path`) resolved at
use time. Plaintext secrets are never returned by the API; a non-secret fingerprint (first 12 hex of SHA-256 under a
keyed MAC) lets clients detect changes.

**Consequences.** Backups contain ciphertext only; restoring requires the KEK. Losing the KEK loses stored secrets
(not telemetry).

## ADR-0008 — Observation model

Observations are typed rows keyed by `(vehicle, signal, observed_at, value_hash)`:
- `observed_at` = upstream `timestampUtc` (or the delivery's capture time when the layout carries none, flagged
  `time_basis = delivery`);
- `received_at` = when WagenPulse stored the delivery; `published_at` (delivery) = portal `createdOn` or the member
  name's timestamp when available;
- values are stored as `num` (REAL: numbers, booleans as 0/1, durations in seconds) or `text` (strings, enums), with
  `quality` in `ok | null | unset | invalid | empty`;
- `unit` is the normalized unit when the dictionary unit is recognized, otherwise the raw unit text;
- a raw value is kept only when normalization changed or rejected it.

Duplicates (same key) are ignored and counted. A different value at the same `observed_at` is a **correction**: both
rows are kept and the later receipt wins in latest state.

## ADR-0009 — Latest-known state precedence

`latest_state` is updated only when the incoming `observed_at` is newer, or equal with a later receipt (correction).
Late observations are stored in history but never replace newer state. Freshness is `now - observed_at` and becomes
`stale` past a per-signal threshold (default 2 hours).

## ADR-0010 — Roles and the root user

Roles: `root` (exactly one user, created at bootstrap, cannot be demoted, suspended or deleted through the API),
`admin`, `operator`, `viewer`. Permissions are fine-grained (`vehicles.read`, `rules.write`, `tokens.manage`, …) and
mapped from roles in `internal/auth/rbac`. Every API operation declares one permission and is checked server-side;
a test asserts that every operation has a permission. All authenticated users see all vehicles (single-household
model); per-vehicle restriction is available on API tokens.

## ADR-0011 — Local authentication

HallPass pattern: Argon2id (m=64 MiB, t=3, p=2) PHC strings with transparent rehash; NFKC normalisation; length 12..1024;
throttling after `login.free_failures` failures per account and per source (IPv6 /64) with exponential delay capped at
`login.max_delay` and decay; server-side sessions storing only HMAC(token); `__Host-wgp_session` cookie (HttpOnly,
Secure on https, SameSite=Lax); session rotation on login and privilege change; idle and absolute expiry; CSRF = Origin /
`Sec-Fetch-Site` check plus `X-CSRF-Token` = HMAC(session) for cookie-authenticated mutations. Optional TOTP for local
logins. Bootstrap uses a one-time setup token minted on the host (`wagenpulse admin setup-token`).

## ADR-0012 — OIDC single sign-on

Porch upstream pattern with coreos/go-oidc: providers are runtime database rows (managed by API/UI/Terraform); PKCE
S256, nonce, single-use hashed state (10 minutes), issuer/audience/signature/expiry validation; identities keyed by
`(provider, subject)`; **never** linked by email; linking requires an authenticated session and an explicit "link"
flow; provisioning `preprovisioned` (only linked users may sign in) or `jit` (creates a user with the provider's default
role, never `admin` or `root`). The service refuses any change that would leave root without a usable login method.

## ADR-0013 — Ingestion safety

Uploads and downloaded archives are untrusted. Limits (configurable): upload 50 MiB, extracted 200 MiB, 1000 entries,
compression ratio 100:1, JSON nesting depth 64, per-entry 64 MiB. Rejected: absolute paths, `..`, symlinks, nested
archives, duplicate entry names. Parsing is streaming (`encoding/json` decoder over the entry reader). Raw deliveries are
stored content-addressed (SHA-256) under `data_dir/blobs/` with a retention date. File names are sanitized before
display.

## ADR-0014 — API tokens

Format `wgp_at_<12-char lookup>_<43-char base64url secret>`; only the lookup and HMAC-SHA256(token-key, secret) are
stored. Scopes are explicit permissions; effective permission = owner's *current* role permissions ∩ token scopes,
evaluated per request, so demotion, suspension or deletion of the owner immediately reduces or removes token power.
Optional per-vehicle restriction. Expiry default 90 days, max 366 days; non-expiring tokens only by root with an
explicit `never_expires` + confirmation. Minting tokens requires the `tokens.delegate` scope on token-authenticated
requests; it is never implied and root-only powers (non-expiring tokens, root changes) are never delegable.
Tokens are accepted only as `Authorization: Bearer`; query-string credentials are rejected. A request with both a bearer
token and a session cookie is authenticated by the token only (privileges never merge). `last_used_at` is buffered in
memory and flushed every 60 s. Revoking the credential used by the current request requires
`confirm_self_revoke=true`.

## ADR-0015 — Event pipeline and transactional outbox

Ingestion, rule evaluation, event creation and routing happen in the **same write transaction** as the observations
they derive from; per-adapter `deliveries` rows are the outbox. Dispatchers run outside transactions, lease deliveries
(`processing` with `lease_until`), publish, and record an attempt. Crashes leave leases that expire and are retried.
Guarantee: at-least-once per (event, adapter); consumers deduplicate on `event_id`. A failing adapter only accumulates
its own deliveries.

## ADR-0016 — Pulse/NATS mapping

Verified against `../pulse` (go-pulse v0.1.3) and `../pulse-github`:
- publish with the go-pulse SDK over JetStream to stream `EVENTS`; a `PubAck` means "stored by JetStream". Pulse offers
  no consumer-processing acknowledgement to producers; WagenPulse calls the state `succeeded (stored by Pulse)` and never
  claims consumer processing;
- subject `evt.<domain>.vehicle.<event_kind>.<vehicle_slug>` by default (configurable template, validated with
  `envelope.ValidateSubject`); tokens encoded like pulse-github `events.Token`;
- `ce-type` `wagenpulse.<event_kind>.v1`, `ce-source` `wagenpulse/<installation id>`, `ce-time` = observation time;
- `Nats-Msg-Id` = deterministic hash of (event ID, adapter ID, replay sequence) — retries within JetStream's 2-minute
  window deduplicate; replays use a new sequence so they publish again;
- credentials: Pulse operator-mode `.creds` (JWT + seed) loaded in memory via `nats.UserJWTAndSeed`; TLS with optional
  CA; a missing publish grant manifests as a timeout and is reported as `publish_timeout (check grants)`.

## ADR-0017 — Backfill and replay policy

Each import carries an event policy: `none` (default for uploads and historical requests), `preview` (evaluate rules,
store nothing, report would-be events) or `publish` (events marked `backfill: true`). Routes have `include_backfill`
(default false), so even published backfill events reach only destinations that opted in. Replays create new delivery
rows marked `replay` for a bounded, previewed selection and require confirmation.

## ADR-0018 — API conventions

Code-first OpenAPI 3.1 with huma v2 (Porch), served under `/api/v1`, committed as `api/openapi.json` and checked for
drift; TypeScript types generated with openapi-typescript. RFC 9457 problem documents with stable codes; cursor
pagination `{items, next_cursor}` (limit 1–200); `ETag`/`If-Match` (412 on mismatch) on every mutable resource;
`Idempotency-Key` on creates (24 h); `X-Request-ID` on every response; strict JSON (unknown fields rejected); explicit
`null` vs omitted semantics documented per field. Asynchronous work is represented by `imports` and `operations`
resources with `queued | running | succeeded | failed | cancelled` states.

## ADR-0019 — Outbound connection policy (SSRF)

Adapter and OIDC endpoints are resolved and checked at connect time: link-local, multicast, unspecified, cloud metadata
(169.254.169.254, fd00:ec2::254) are always refused; loopback is refused unless in `outbound.allow_cidrs`; private
ranges are allowed by default (`outbound.allow_private`, self-hosted NATS usually lives on a LAN) and can be denied with
`outbound.deny_cidrs`. NATS URLs must be `nats://` or `tls://`; production refuses plaintext `nats://` to non-loopback
hosts unless the adapter sets `allow_insecure_transport`.

## ADR-0020 — Live UI updates by polling

TanStack Query polling with adaptive intervals (2 s while an import/operation runs, 15–30 s otherwise). Upstream data
changes at most every 15 minutes; polling works through every reverse proxy and needs no connection state. SSE can be
added later for operations if needed.

## ADR-0021 — Frontend stack

React 19, strict TypeScript, Vite, TanStack Router (file-based) / Query / Form / Table / Store (shell UI state only),
Mantine 8 (MIT; accessible primitives, theming via CSS variables, used by Porch and Pulse), Tabler icons (MIT),
ECharts 6 (Apache-2.0; large-dataset performance, gap rendering, used by HallPass and Pulse). Cobalt2-derived design
tokens for light and dark themes with a contrast test. The UI is embedded into the binary.

## ADR-0022 — Terraform provider in a separate repository

The sibling convention is one repository per provider (`terraform-provider-<name>`), and the project owner chose it
(2026-11-02). The provider lives in `../terraform-provider-wagenpulse`, follows terraform-provider-deed, uses only the
public API through its own client and pins the app version used for acceptance tests.

## ADR-0023 — Deletion semantics

- Vehicle `DELETE` archives: configuration is hidden from active lists and collection stops; telemetry, events and
  deliveries are retained. `POST /vehicles/{id}/purge` (root/admin, explicit confirmation, audited) deletes telemetry.
- Source `DELETE` stops collection and removes credentials; imported data remains.
- Adapter `DELETE` is refused while deliveries are pending/retrying unless `abandon_pending=true` (they become `dead`
  with reason `adapter_deleted`); delivery history remains.
- Rule `DELETE` soft-deletes: revisions and events remain for history.
- User deletion revokes sessions and tokens; root cannot be deleted.

## ADR-0024 — Retention

Retention settings (singleton, API/Terraform): observations (default 730 days, 0 = keep forever), raw deliveries
(90 days), events and deliveries (180 days), audit (730 days, never less than 90), finished jobs/imports (90 days).
A daily job deletes in bounded batches and runs `incremental_vacuum`. Rollups are kept as long as observations.

## ADR-0025 — Backup and restore

`wagenpulse backup create` uses SQLite's online backup (`VACUUM INTO`) plus a copy of the blob directory into a
timestamped directory with a manifest of SHA-256 sums; `backup verify` checks integrity and the manifest; `restore`
refuses to run while the data directory is locked and requires an empty target. The KEK is never included.

## ADR-0026 — Rule evaluation semantics

- Rules evaluate per (rule, vehicle, signal) in observation-time order inside the ingest transaction.
- Observations older than the rule state's `last_observed_at` are stored but do not fire (late arrivals never produce
  out-of-order events). This is reported in the import summary as `late_skipped`.
- Editing a rule creates a new immutable revision and resets that rule's state; events reference the revision.
- `stale` rules are evaluated by a durable scheduled sweep; each (rule, vehicle) fires once per stale episode and
  re-arms when fresh data arrives.
- Debounce requires the condition to hold for N consecutive observations; cooldown suppresses repeats for a duration.

## ADR-0027 — Administrative commands attach to a running server

The data-directory `flock` keeps two servers (and their schedulers) from running on one database. The first e2e run
showed that it also refused `wagenpulse admin setup-token` while the server ran. That forced an operator to stop the
service to bootstrap it or to recover a locked-out root user.

- `serve`, `migrate`, `keys rotate|retire` and `restore` keep taking the exclusive lock. Key rotation changes the
  keyring the server holds in memory, and migrations change the schema under it.
- `admin setup-token`, `admin reset-password`, `audit verify` and `keys status` open the store with `AllowAttach`:
  - With no server running, they take the lock as before.
  - With a server running, they attach without the lock and never migrate. The schema version must equal the binary's
    exactly, and the command never creates keyring keys.
- These commands keep no in-memory state that the server relies on. Their writes are single short transactions
  (`BEGIN IMMEDIATE`, busy timeout). The audit chain head is read inside the transaction, so appends from both
  processes stay ordered and the chain stays valid.
- Running a long-lived second writer is still unsupported.
