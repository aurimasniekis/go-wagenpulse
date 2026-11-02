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
| M0 | Planning docs, repo hygiene, tooling, CI skeleton | in progress | |
| M1 | Core platform: config, logging/redaction, store + migrations, ids, apperr, keyring/secretref, health/metrics, `serve`, graceful shutdown | todo | |
| M2 | Auth: bootstrap, local login, sessions, CSRF, throttling, TOTP, RBAC, API tokens, audit, OIDC; web shell + admin screens | todo | |
| M3 | Terraform provider repo scaffold + client + acceptance harness; user/token/settings/OIDC resources | todo | |
| M4 | Dictionaries: dictgen, bundled JSON, versioned tables, unit registry, signals API | todo | |
| M5 | Vehicles, sources, jobs/scheduler, safe importer, parser, normalization, observations/state; imports UI; vehicle/source resources | todo | |
| M6 | Unofficial portal collector (cookie + scripted login), auth states, rate limiting, checkpoints; collection health UI | todo | |
| M7 | Queries/aggregation/rollups; fleet, vehicle, explorer screens; CSV export | todo | |
| M8 | Rules, events, routes, dispatcher, Pulse adapter; rules/adapters/events UIs; provider resources | todo | |
| M9 | Retention, purge, backup/restore, audit UI, system info; retention resource | todo | |
| M10 | Hardening: benchmarks, race, crash/restart, e2e + axe in both themes, docs, Dockerfile, release, provider example, final report | todo | |

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
