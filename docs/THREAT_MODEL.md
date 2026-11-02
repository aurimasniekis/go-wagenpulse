# Threat model

## Assets

| Asset | Sensitivity | Where |
|---|---|---|
| Vehicle telemetry, VINs, timelines | Personal data (behaviour, presence) | `observations`, `latest_state`, raw blobs |
| VW Identity credentials / portal session cookies | Account takeover of the vehicle owner's VW account | sealed in `sources` |
| Pulse `.creds` (NATS JWT + seed) | Publish rights to the event bus | sealed in `adapters` or external refs |
| OIDC client secrets | SSO impersonation of WagenPulse | sealed in `oidc_providers` |
| Password hashes, TOTP secrets | Local account takeover | `users` |
| Session and API tokens | Full or scoped API access | only HMACs stored |
| KEK | Decrypts every stored secret | outside the data directory |
| Audit log | Accountability | `audit_events` (hash chain) |

## Trust boundaries

1. Browser ↔ WagenPulse (TLS terminated by WagenPulse or a reverse proxy).
2. Automation (Terraform, scripts) ↔ API with bearer tokens.
3. WagenPulse ↔ VW portal / VW Identity (outbound, unofficial interface).
4. WagenPulse ↔ NATS/Pulse (outbound).
5. WagenPulse ↔ OIDC providers (outbound).
6. Uploaded files (untrusted content).
7. Host operator (CLI, data directory, KEK) — trusted.

## Threats and mitigations

| # | Threat | Mitigation | Verified by |
|---|---|---|---|
| T1 | Password guessing | Argon2id, throttling per account and source, uniform error, audit | `internal/auth/local` tests |
| T2 | Session theft / fixation | HttpOnly `__Host-` cookies, rotation on login/privilege change, HMAC-only storage, idle/absolute expiry, revoke | session tests |
| T3 | CSRF | Origin/Sec-Fetch-Site check + `X-CSRF-Token` HMAC on cookie mutations; bearer requests are not cookie-authenticated | API tests |
| T4 | Token leakage | Prefix for secret scanning, shown once, HMAC verifier, Bearer-only, never logged, rejected in query strings, expiry, immediate revocation | token tests, log redaction tests |
| T5 | Privilege escalation via tokens | effective = owner's current permissions ∩ scopes; `tokens.delegate` never implied; root-only operations not delegable; no-expiry only for root | rbac/token tests |
| T6 | Root lockout | Root undeletable/undemotable; login-method changes refuse to remove root's last method | settings tests |
| T7 | OIDC attacks (CSRF, replay, mix-up, code injection) | state single-use hashed, nonce, PKCE S256, issuer/audience/alg checks, no email linking | oidc tests (fake IdP) |
| T8 | Malicious archive (zip bomb, traversal, symlink) | size/entry/ratio limits, path validation, streaming parse, no extraction to disk | ingest tests, fuzz |
| T9 | SSRF through adapter/OIDC URLs | scheme allowlist, resolved-IP policy, metadata/link-local always blocked | netguard tests |
| T10 | Secret disclosure through API/Terraform state | secrets write-only, fingerprints only, external refs supported, sensitive/write-only attributes in the provider | API + provider tests |
| T11 | Telemetry over-sharing to event consumers | envelopes carry only rule-selected values; VIN excluded unless the adapter opts in; payload inspection redacts | event tests |
| T12 | Upstream account lockout / ToS breach by aggressive polling | ≥15 min polling, Retry-After, back-off, honest User-Agent, no priming, opt-in | collector tests |
| T13 | Log leakage | slog redaction of sensitive keys; VINs masked in logs; tokens and cookies never logged | redact tests |
| T14 | Audit tampering | hash chain + `wagenpulse audit verify` (detects in-DB edits; a DB-level attacker rewriting the whole chain is out of scope) | audit tests |
| T15 | Data loss by accidental deletion | archive vs purge separation, confirmation fields, audit | API tests |
| T16 | Denial of service via expensive queries | time-range and point limits, server-side downsampling, request timeouts, per-token rate limits | query tests |

## Non-goals

- Legal compliance claims (GDPR, EU Data Act). WagenPulse provides retention and deletion controls; compliance is the
  operator's responsibility.
- Protection against a host administrator with access to both the data directory and the KEK.
- Multi-tenant isolation (single household/fleet).
