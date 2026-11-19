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

- Do not work on `main`. Start each task on its own feature branch (for example `feat/…`, `fix/…`, `docs/…`) and keep
  all of its commits there.
- One Conventional Commit per verified milestone or coherent sub-slice. Conventional Commits apply to every commit
  message, including those on feature branches (see `CONTRIBUTING.md`).
- Stage only relevant files by path; inspect the diff first.
- Commit with the git identity and signing profile the operator configures for agents, never a personal one.
- Push only your feature branch, and only to the remote the task names. Never push to `main` or any other protected
  branch, never force-push, and never delete remote branches.
- When the task is ready for review, open a pull request from your branch against `main`. Describe what changed, how
  it was verified, and what is still open. Do not open a pull request for work that has not been verified.
- A maintainer reviews and merges pull requests and publishes releases. Never merge a pull request or publish a
  release yourself. Create or push a release tag (`docs/RELEASING.md` step 3) only after a maintainer has explicitly
  approved that version.
