# Releasing WagenPulse

Adapted from Porch docs/RELEASING.md (Apache-2.0, same author).

Versions follow [Semantic Versioning](https://semver.org/) with a `v` prefix: `vMAJOR.MINOR.PATCH`, with an optional
`-rc.N` or similar prerelease suffix. A release is cut from `main` by pushing a `vX.Y.Z` tag. The
[release workflow](../.github/workflows/release.yaml) does the rest and stops at a **draft** GitHub release, which a
maintainer reviews and publishes.

> **Nothing has been released yet.** The workflow has been validated with actionlint, and its building blocks
> (`scripts/release-build.sh`, the Dockerfile, `scripts/container-smoke.sh`) have been run locally. The workflow
> itself has not run on GitHub. WagenPulse has had no external security review; keep a "Not externally reviewed"
> note in each release's CHANGELOG section.

## What the workflow does

| Job | What it does |
|---|---|
| `meta` | Checks the tag format and that `CHANGELOG.md` has a `## [X.Y.Z] - YYYY-MM-DD` section; derives the version and `SOURCE_DATE_EPOCH` |
| `test` | `make vet race` |
| `web` | `pnpm install --frozen-lockfile && pnpm build` in `web/` |
| `binaries` | `scripts/release-build.sh` builds static archives (linux/amd64, linux/arm64, darwin/arm64) with the UI embedded; smoke-tests `version --json`; produces SPDX SBOMs (syft) and `SHA256SUMS` |
| `image-scan` | Builds the image per architecture; Trivy fails on fixable HIGH/CRITICAL findings; runs `scripts/container-smoke.sh` on amd64 |
| `image-publish` | Tag runs only. Pushes the multi-arch image to `ghcr.io/aurimasniekis/go-wagenpulse` (and Docker Hub if `DOCKERHUB_TOKEN` is set) with provenance and SBOM, then runs Trivy on the pushed digest |
| `attest-binaries` | Tag runs only. Writes build provenance attestations over `SHA256SUMS` |
| `github-release` | Tag runs only. Creates a draft release with the CHANGELOG section, image digest and assets |

## Checklist

1. `main` is green; run `make check` and `make e2e` locally.
2. Move the `[Unreleased]` entries in `CHANGELOG.md` to `## [X.Y.Z] - YYYY-MM-DD` and commit.
3. Tag and push: `git tag -s vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z`.
4. Watch the workflow, review the draft release notes and assets, then publish.
5. Verify from a clean machine: `sha256sum --check SHA256SUMS`, `gh attestation verify <file> --repo
   aurimasniekis/go-wagenpulse`, and run the published image through `scripts/container-smoke.sh`.

## Local dry run

```sh
make build
VERSION=v0.0.0-test scripts/release-build.sh -o /tmp/wagenpulse-dist linux/amd64 darwin/arm64
docker build --build-arg VERSION=v0.0.0-test -t wagenpulse:test . && scripts/container-smoke.sh wagenpulse:test
```
