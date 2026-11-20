# Implementation report

Status as of 2026-11-02. Nothing has been pushed, tagged, released or published.

## Delivered

**Application** (`go-wagenpulse`, Go 1.27.4 with an embedded React/TypeScript UI):

- **Import**
  - Content-addressed raw storage and safe ZIP/JSON extraction (size, entry, ratio, traversal, symlink and nesting
    limits).
  - Format detection with the verified `vwdataact.continuous.v1` parser. Unknown formats are kept as `unrecognized`
    for later reprocessing.
  - Normalization against the bundled dictionaries (1,141 continuous and 5,140 historical keys); unknown keys are kept
    as `observed`.
  - Typed observations with dedupe and corrections, latest state with late-arrival precedence, and hourly rollups.
- **Collection:** an opt-in collector for the **unofficial** portal interface, using a session cookie or scripted
  login. It has authorization states, a rate limiter, backoff, checkpoints and an honest User-Agent, and it never
  bypasses CAPTCHA, consent or terms prompts.
- **Rules and events**
  - Rule types: threshold with hysteresis, change with deadband, transition, and stale data (scheduled sweep).
  - Debounce, cooldown, immutable revisions and preview.
  - Events in a transactional outbox, routed to Pulse/NATS adapters.
  - At-least-once delivery with per-adapter workers, retry, dead letters, manual retry and replay.
- **Access**
  - Setup-token bootstrap of the single root user.
  - Argon2id passwords with throttling, TOTP with recovery codes, and sessions with CSRF protection.
  - Roles: root, admin, operator and viewer, enforced per API operation.
  - API tokens whose permissions are the role ∩ scopes. Never-expiring tokens are root-only and interactive, and
    delegation is never implied.
  - OIDC with PKCE, nonce and explicit linking.
- **Operations**
  - KEK-wrapped envelope encryption and a hash-chained audit log.
  - Retention, online `backup create|verify` and `restore`, and `keys rotate|retire|rewrap`.
  - Administrative commands that work while the server runs.
  - Prometheus metrics, health endpoints, and immediate crash recovery of leased work.
- **UI:** every screen from the plan, in Cobalt2-style light and dark themes with AA-checked colour tokens, and real
  loading, empty, error and stale states.
- **Packaging**
  - A distroless non-root image, a Compose deployment and a hardened systemd unit.
  - Reproducible release archives and a release workflow.
  - Operator guides, README, CHANGELOG, CONTRIBUTING and SECURITY.

**Terraform provider** (`terraform-provider-wagenpulse`, separate local repository):

- 10 resources: vehicle, source, event_rule, pulse_adapter, route, retention_settings, system_settings,
  oidc_provider, user, api_token.
- 4 data sources: vehicle, signal, server_info, source_status.
- Write-only secrets, an acceptance harness built from a `git archive` of the app, generated docs, goreleaser
  (unsigned snapshot only) and CI workflows.

## Architecture decisions

ADR-0001 to ADR-0027 are in [DECISIONS.md](DECISIONS.md). The most consequential:

- SQLite with a single writer (ADR-0002; the measurements are in [STORAGE.md](STORAGE.md)).
- Explicit separation of verified and unofficial upstream behaviour ([CAPABILITIES.md](CAPABILITIES.md)).
- The transactional outbox with at-least-once delivery and a deterministic `Nats-Msg-Id` (ADR-0015).
- Rule evaluation in observation-time order, where late data never fires (ADR-0026).
- Online backups (ADR-0025).
- Administrative commands attaching to a running server (ADR-0027).

## Verification (commands run; final results)

| Command | Result |
|---|---|
| `go test ./...` (strict 100 ms transaction budget) | pass, 21 packages |
| `make race` | pass, 21 packages. Earlier runs failed only the load-sensitive transaction-time guard while the machine was heavily loaded |
| `make lint vet fmt-check sqlc-check api-check` | 0 issues, no drift |
| `make vuln` | 0 reachable vulnerabilities. One advisory in `golang.org/x/crypto` (GO-2026-5932) has no fix and is not called |
| `make secrets` (gitleaks) | no leaks |
| `make web-check` | lint and typecheck clean, 90 unit/contrast tests, build OK |
| `make e2e` | 52/52 Playwright tests in light and dark themes with axe (WCAG 2.1 AA): every page plus sign-in, history chart/table, duplicate upload, rule preview, token create/revoke and 2FA enrolment |
| `TestRealSamples` (owner's real deliveries, local only) | 17 files → 1 delivery + 16 byte-identical duplicates; 64 observations; 62 dictionary + 2 observed signals |
| `TestCrashDuringImportRecovers` | Real server SIGKILLed at 5/80 files; resumed 5.6 s after restart; 72,000/72,000 observations stored exactly once |
| `TestCrashMidDeliveryIsRecovered` | Re-published message acknowledged as a JetStream duplicate (stored once); unpublished one delivered |
| `make bench` | 15.6k observations/s ingest; charts 2.4–27 ms for 24 h to 1 year (after fixing a quadratic query that took ~1 s) |
| Backup CLI smoke test | Backup while running, verify, tamper detected (exit 3), restore, restart on restored data, non-empty target refused |
| `scripts/container-smoke.sh` | PASS: non-root, no shell, production config on read-only root, KEK refusals, healthy, API + UI served, clean SIGTERM |
| `scripts/release-build.sh` (two platforms) | Archives and `SHA256SUMS` verified |
| `actionlint` | CI and release workflows clean (the workflows have not run on GitHub) |
| Provider `make lint test` | 0 issues; unit tests pass |
| Provider `make testacc` against app `8bdb782` | 54/54 (20 acceptance + 34 unit) |
| Provider `make scenario` (`examples/complete`) | 11 added → no changes → 11 destroyed |

## Terraform status

All durable configuration entities are covered and acceptance-tested against a locally built app; see
[TERRAFORM_COVERAGE.md](TERRAFORM_COVERAGE.md). The provider's tests found three app bugs, which are fixed:

- `429` responses had no `Retry-After`;
- tokens returned `vehicle_ids: null`;
- the first settings write reused revision 1.

Known limitations:

- archived vehicles keep their slug (use import to restore);
- some secrets cannot be cleared;
- root-only interactive actions are out of reach for tokens.

**Not published.** There has been no registry publication, GPG signing or tagging.

## Upstream limitations

- **No official API exists.** The collector uses the portal's undocumented web backend. It was tested only against a
  synthetic imitation and **has not run against the live portal**.
- Data requests and consents can only be created interactively. The best cadence is about 15 minutes, there is no
  push, and requests renew about every 12 months.
- **The historical export format is unverified.** No sample exists, so such files are stored as `unrecognized`.
- The bundled dictionaries are Volkswagen AG documents. You accepted the redistribution risk.

## Risks

- The portal may change or block the unofficial interface at any time. Uploads remain the supported path.
- SQLite with a single writer suits the planned 1–20 vehicles. [STORAGE.md](STORAGE.md) lists the triggers for
  moving off it.
- There has been no external security review.
- The systemd unit has not been run under a real systemd.
- The CI and release workflows have not run on GitHub.

## Commits

- **App (`main`):** `165c671`, `a1efa95`, `8586838`, `de221a0`, `6a8ed14`, `de60842`, `e2f468a`, `dc71732`,
  `3d5fd4b`, `3523b83`, `d2c18a2`, `974a269`, `8bdb782`, `50b1dd2`, plus this report.
- **Provider (`main`):** `8f790b2`, `8319e75`.

## How to run

```sh
make build && make dev                     # http://localhost:58080 (development mode)
make dev-setup-token                       # in another terminal, then open /setup
```

For production, see [operator/install.md](operator/install.md). For Terraform, build the provider (`make install` in
the provider repo) and use `dev_overrides`, as its README describes.
