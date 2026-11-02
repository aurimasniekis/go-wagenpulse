# Troubleshooting

## Health and metrics

All of these are served on the management listener (`127.0.0.1:9464` by default):

- `/healthz`: the process is alive.
- `/readyz`: the database is writable and the server is ready. `wagenpulse healthcheck` probes it.
- `/metrics`: Prometheus metrics for HTTP, the database writer, imports, collection lag, rule evaluations, events and
  delivery queues per adapter, and logins.

**System** in the UI shows the version, readiness, schema version and loaded dictionaries.

Logs are JSON on stdout (ECS fields). Requests and background work carry a correlation ID. The UI shows it on
imports, and you can search the logs for it.

## Locked out

- **Forgot the root password**: run `wagenpulse admin reset-password --user root` on the host. It prints a new random
  password, re-enables password sign-in for that user and in system settings, and signs out the user's sessions.
  It works while the server runs.
- **Lost the authenticator**: sign in with a recovery code. An admin can also reset another user's two-factor
  authentication under **Users**.
- **No root user yet**: run `wagenpulse admin setup-token` and open `/setup`.

## Common errors

| Symptom | Cause / fix |
|---|---|
| `another wagenpulse process owns this data directory` | A server is already running on this data directory. `migrate`, `keys rotate|retire|rewrap` and `restore` need it stopped; other admin commands work alongside it |
| `master key (KEK) does not match this data directory` | Wrong KEK file for this database. Use the KEK the data was created with, or the new one after `keys rewrap` |
| `must not be accessible by group or others` | `chmod 0400` the KEK file; it must be owned by the service user |
| `unknown environment override(s)` | A `WAGENPULSE_*` variable that is not a setting. Rename it (Compose interpolation uses `WP_`) |
| Uploads fail at the proxy | Raise the reverse proxy's body limit to at least `ingest.max_upload_bytes` |
| Adapter test: *timeout* | The NATS credentials lack a publish grant for the rendered subject (Pulse reports missing grants as timeouts) |
| Adapter: *destination blocked* | The outbound policy refused the address. Add the NATS host to `outbound.allow_cidrs` (loopback) or remove it from `deny_cidrs` |
| Import shows *unrecognized* | Not a known delivery format, for example historical exports. The file is kept; reprocess it after an upgrade adds support |
| Source shows `reauthorization_required` / `no_active_request` | See [collection.md](collection.md#source-states) |
| Charts show gaps | No observations in those buckets. WagenPulse never interpolates. Check the vehicle's imports and the source's lag |

## Audit

**Audit log → Verify chain**, or `wagenpulse audit verify`, checks the hash chain and exits 3 if it is broken. A
broken chain means records were changed or removed outside WagenPulse. Investigate, and restore from a backup if
needed.
