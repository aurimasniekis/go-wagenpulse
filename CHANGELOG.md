# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
- **Unofficial portal collector:** session-cookie or scripted login, authorization states, rate limiting and
  checkpoints. Unverified against the live portal.
- **Rules, events and delivery**
  - Rule types: threshold, change, transition and stale data.
  - Rule revisions and preview.
  - Event outbox, routes, and Pulse/NATS adapters.
  - At-least-once delivery with retry, dead letters and replay.
- **Operations**
  - Retention.
  - Online `backup create`/`verify`, and `restore`.
  - `keys rotate|retire|rewrap`.
  - Administrative commands that work while the server runs.
- **Web UI:** React and Mantine, with Cobalt2-style light and dark themes, AA-checked colours and axe-tested pages.
- **Packaging:** distroless container image, Compose file, systemd unit, release workflow and archives.

[Unreleased]: https://github.com/aurimasniekis/go-wagenpulse/commits/main
