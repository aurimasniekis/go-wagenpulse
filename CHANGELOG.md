# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- The portal collector no longer mistakes an expired portal session for "no active data request". The portal does
  not always answer an expired session with 401: it can redirect into the sign-in flow or answer as if the account
  had nothing. The collector reported such a source as `no_active_request` (a success) and never signed in again, so
  deliveries stopped although the data request was still active. A redirect into the sign-in flow or an HTML page from
  the API now counts as an expired session. When every request reports "no active request", the collector also checks
  whether the session still lists vehicles. In password mode it then signs in again, at most once per run. In cookie
  mode the source moves to `reauthorization_required`.
- A source that reports `no_active_request` for 4 consecutive runs no longer counts as a success. Its last-success
  time stops advancing, the reason gives the number of runs, a warning is logged and retries back off.

## [0.1.1] - 2026-11-20

### Security

- Updated `github.com/go-jose/go-jose/v4` from v4.1.4 to v4.1.6, which fixes CVE-2026-116587 (denial of service via
  crafted RSA parameters in JWKs).

## [0.1.0] - 2026-11-19

> **Not externally reviewed.** WagenPulse has had no external security review. It stores personal vehicle telemetry and
> credentials for third-party accounts; review the security notes in the documentation before relying on it.

### Added

- **Core platform**
  - Configuration file with strict `WAGENPULSE_*` overrides.
  - SQLite storage with migrations and pre-migration snapshots.
  - Envelope encryption under an external KEK.
  - Hash-chained audit log.
  - Prometheus metrics and health endpoints.
  - Outbound network policy (SSRF guard).
- **Authentication**
  - Setup-token bootstrap of the root user.
  - Local login: Argon2id, throttling, TOTP with recovery codes.
  - Sessions with CSRF protection.
  - Roles: root, admin, operator, viewer.
  - Scoped API tokens; never-expiring tokens are root-only.
  - OpenID Connect single sign-on with explicit identity linking.
- **Telemetry**
  - Bundled EU Data Act dictionaries (continuous and historical).
  - Safe ZIP/JSON delivery import with content-addressed dedupe.
  - Typed observations, latest state, and hourly rollups.
  - Charts with explicit gaps, a data explorer and CSV export.
  - Historical ("all") export format is unverified; archives that do not match a known structure are stored as
    `unrecognized` and never decoded into invented values.
- **Unofficial portal collector:** not an official API. Session-cookie or scripted login, authorization states, rate
  limiting and checkpoints. Scripted login (Auth0) and collection of a continuous delivery were verified live by the
  project owner on 2026-11-02, with one account and one vehicle. The session-cookie mode and session lifetime are
  not yet verified live.
- **Rules, events and delivery**
  - Rule types: threshold, change, transition and stale data.
  - Rule revisions and preview.
  - Event outbox, routes, and Pulse/NATS adapters.
  - At-least-once delivery with retry, dead letters and replay.
- **Operations**
  - Retention.
  - Online `backup create`/`verify`, and `restore` (server stopped).
  - `keys rotate|retire|rewrap` (server stopped).
  - Administrative commands that work while the server runs: `admin setup-token`, `admin reset-password`,
    `audit verify`, `keys status` and `backup create`.
- **Web UI:** React and Mantine, with Cobalt2-style light and dark themes, AA-checked colours and axe-tested pages.
  Vehicles are shown as readable dashboards rather than raw signal dumps.
- **Packaging:** distroless container image, Compose file, systemd unit, release workflow and archives.

### Fixed

- Continuous dictionary errata: the door and safety code descriptions that the published dictionary gets wrong are
  decoded correctly (`open_state_front_right_door`, `open_state_rear_right_door`, `safe_state_front_engine_bonnet`,
  `safe_state_tailgate`). Stored values are never changed.
- The collector no longer mistakes Auth0's captcha scripts for a bot challenge. A challenge is reported only when the
  form demands a captcha, a challenge page is served instead of the form, or a submission is rejected.
- Sources that are signed in but have no vehicles selected now say so explicitly.
- An administrative command started while the server is still starting no longer fails with a dictionary
  UNIQUE-constraint error.
- API contract: 429 responses carry `Retry-After`; unrestricted API tokens return `vehicle_ids: []` instead of null;
  the first stored settings write gets revision 2 (not the defaults' revision 1), so `If-Match` detects a concurrent
  first write.
- Loading the bundled dictionaries writes in smaller batches, so no single write transaction holds the database for
  long (ADR-0002).

### Security

- Built with Go 1.27.4, which fixes GO-2026-6859 (an HTTP/1 server crash caused by a `MaxBytesReader` header race in
  `net/http`). The container's build image is updated to match.

[Unreleased]: https://github.com/aurimasniekis/go-wagenpulse/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/aurimasniekis/go-wagenpulse/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/aurimasniekis/go-wagenpulse/releases/tag/v0.1.0
