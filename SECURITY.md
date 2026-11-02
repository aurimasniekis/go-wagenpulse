# Security policy

WagenPulse stores vehicle telemetry (including location-related data, where present) and credentials for third-party
accounts. Please report vulnerabilities privately.

## Reporting

Use GitHub's private vulnerability reporting ("Report a vulnerability" on the repository's Security tab). Do not open
a public issue.

Include:

- affected version or commit;
- impact;
- reproduction steps.

Never include real VINs, telemetry, portal credentials or tokens; synthetic examples are enough.

## Supported versions

Before 1.0, only the latest release receives fixes.

## Scope and design

[docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) describes the threat model and the controls in place. In brief:

- stored secrets are encrypted under a KEK kept outside the data directory;
- authorization is enforced server-side for every API operation;
- outbound connections pass an SSRF guard;
- the audit log is hash-chained;
- the portal collector never bypasses CAPTCHA, consent or terms.

The project has **not** had an external security review.
