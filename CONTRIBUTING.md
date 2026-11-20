# Contributing

Read [AGENTS.md](AGENTS.md) first: its rules apply to humans and agents alike. Also read
[docs/DECISIONS.md](docs/DECISIONS.md), which records design decisions as ADRs.

## Setup

- Go 1.27.4, Node 24 and pnpm 10 (`corepack enable`).
- `make build` builds the UI and the binary; `make dev` runs a development instance.
- `cd web && pnpm dev` serves the UI with hot reload, proxying the API to `make dev`.

## Before sending a change

- `make check` runs, among other things:
  - gofmt, vet, golangci-lint and race tests;
  - sqlc and OpenAPI drift checks;
  - govulncheck and gitleaks;
  - web lint, typecheck, unit tests and build.
- `make e2e` runs Playwright with axe in light and dark themes when you touch the UI.
- After changing the API, run `make api` and commit `api/openapi.json` and `web/src/api/schema.gen.ts`.
- After changing queries or migrations, run `make sqlc`.

## Rules that matter most

- **No real data.** Never commit VINs, account identifiers, credentials or real telemetry. Real samples go in the
  git-ignored `testdata/local/`; fixtures are synthetic.
- **No silent security relaxations.** Any weakening needs an ADR labelled *security-relevant change*.
- **No invented upstream behaviour.** Record upstream evidence in [docs/CAPABILITIES.md](docs/CAPABILITIES.md) and
  keep unverified behaviour labelled as unverified.
- **Write transactions do no network I/O.**
- **Every API operation declares a permission.**
- **Adapted code keeps a provenance comment.**
- **Commits:** use [Conventional Commits](https://www.conventionalcommits.org/).
