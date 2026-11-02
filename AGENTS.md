# AGENTS.md — rules for agents working on WagenPulse

WagenPulse stores personal vehicle telemetry and credentials for third-party accounts. These rules apply to every
agent session in this repository.

## Before working

1. Read `docs/PLAN.md` (milestones, status, definition of done) and `docs/DECISIONS.md` (ADRs).
2. Read `docs/CAPABILITIES.md` before touching acquisition code: never claim an upstream capability without evidence.
3. Consult `docs/THREAT_MODEL.md` for the security obligations of the area you touch.

## Contract rules

- **Never silently weaken security requirements.** Any relaxation is an ADR labelled **security-relevant change**.
- **Distinguish implemented, tested and externally verified behaviour.** Upstream (VW portal) behaviour stays
  "unverified" until an operator confirms it; record evidence in `docs/CAPABILITIES.md` without VINs or values.
- No fake success states, placeholder data or dead UI controls. Demo data is synthetic, labelled and development-only.
- Never commit secrets, real VINs, account identifiers or real telemetry. Real samples live in the git-ignored
  `testdata/local/`; committed fixtures are synthetic.
- Write transactions (`store.Writer.Tx`) do no network I/O, sleeps or channel operations.
- Every API operation declares a permission; authorization is enforced server-side.
- Adapted code from Porch/HallPass/Pulse/pulse-github/go-cupra keeps a provenance comment.
- Do not modify sibling repositories (`../porch`, `../hallpass`, `../pulse`, …).

## Git workflow

- Work on `main`. One Conventional Commit per verified milestone or coherent sub-slice (`/cc`).
- Stage only relevant files by path; inspect the diff first.
- **Never push**, create tags or open PRs without an explicit instruction.
