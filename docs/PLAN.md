# WagenPulse plan and status

This file tracks milestones, their status and the evidence for "done". It is the resume point across sessions.
Statuses: `todo`, `in progress`, `done` (implemented + tested + reachable), `blocked` (with the blocker).

## Goal

A production-grade, self-hosted application that imports and collects Volkswagen Group EU Data Act vehicle data,
stores it as typed, provenance-tracked observations, visualizes it, and turns selected signals into durable events
delivered independently to any number of Pulse/NATS adapter instances — with local and OIDC authentication, API
tokens, a polished UI and a Terraform provider covering all durable configuration.

Key references: `docs/DECISIONS.md`, `docs/CAPABILITIES.md`, `docs/STORAGE.md`, `docs/THREAT_MODEL.md`,
`docs/TERRAFORM_COVERAGE.md`, `docs/ARCHITECTURE.md`.

## Milestones

| # | Scope | Status | Evidence |
|---|---|---|---|
| M0 | Planning docs, repo hygiene, tooling, CI skeleton | done | commit 165c671 |
| M1 | Core platform: config, logging/redaction, store + migrations, ids, apperr, keyring/secretref, health/metrics, `serve`, graceful shutdown | done | `go test ./...`, golangci-lint 0 issues |
| M2 | Auth: bootstrap, local login, sessions, CSRF, throttling, TOTP, RBAC, API tokens, audit, OIDC; web shell + admin screens | done | internal/auth, internal/auth/oidc tests; e2e sign-in, token and 2FA flows |
| M3 | Terraform provider (separate repo `../terraform-provider-wagenpulse`) | in progress | see the provider repo's README and test results |
| M4 | Dictionaries: dictgen, bundled JSON, versioned tables, unit registry, signals API | done | internal/dictionary tests; 1141/5140 entries |
| M5 | Vehicles, sources, jobs/scheduler, safe importer, parser, normalization, observations/state; imports UI | done | internal/ingest tests incl. real samples (local only); e2e upload + duplicate detection |
| M6 | Unofficial portal collector (cookie + scripted login), auth states, rate limiting, checkpoints; collection health UI | done, **not live-verified** | internal/sources tests against fakeportal; sources pages in e2e |
| M7 | Queries/aggregation/rollups; fleet, vehicle, explorer screens; CSV export | done | rollup-vs-raw test; benchmarks in docs/STORAGE.md; e2e vehicle history chart/table |
| M8 | Rules, events, routes, dispatcher, Pulse adapter; rules/adapters/events UIs | done | internal/adapters integration tests (delivery, missing grant, outage, replay, crash mid-delivery); e2e rule preview |
| M9 | Retention, purge, backup/restore, audit UI, system info | done | internal/backup tests; CLI smoke test (backup while running, tamper detection, restore, restart) |
| M10 | Hardening: benchmarks, race, crash/restart, e2e + axe in both themes, docs, Dockerfile, release, final report | done except the final report | `make bench`; `TestCrashDuringImportRecovers`; 52 Playwright tests; `scripts/container-smoke.sh` PASS; release-build dry run; actionlint |

## Definition of done

See the task brief §16; tracked item by item in the final section of this file at M10.

## Blockers and external dependencies

| Item | Status | Needed to resolve |
|---|---|---|
| Historical export format | unverified | a real historical export sample (docs/CAPABILITIES.md) |
| Live validation of the unofficial portal collector | unverified | operator run with own credentials |
| Registry publication / signing of the provider | not performed | explicit authorization, GPG key, registry namespace |

## Session log

- 2026-11-02 — Investigation of sibling repos (hallpass, porch, pulse, pulse-github, cupra, terraform-provider-*),
  upstream research, owner-supplied dictionaries and sample deliveries. Decisions recorded (ADR-0001…0026).
- 2026-11-02 — Backend M1–M9; web UI (all screens), Playwright + axe suite in both themes; attach mode for admin
  commands (ADR-0027); online backup/verify/restore; benchmarks (fixed a quadratic rollup query: 1 s → 27 ms);
  crash recovery (orphaned leases released at startup) with process-level and delivery crash tests; `keys rewrap`;
  container image, Compose, systemd unit, release pipeline and operator docs.
