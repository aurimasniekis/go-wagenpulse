# Terraform configuration coverage

The provider lives in `../terraform-provider-wagenpulse` (ADR-0022) and uses only the public API. This matrix maps
every durable, user-configurable entity to its API and Terraform representation. Status columns are updated only with
evidence from acceptance tests.

"acceptance-tested" means the provider's `make testacc` passed against app commit `8bdb782` (Terraform 1.17.0): create,
empty plan, update/replace, import, out-of-band drift and deletion. The `examples/complete` apply → empty plan → destroy
scenario also passed. The provider is not published.

| Entity | API | Terraform | Import ID | Lifecycle / deletion | Secrets | Status |
|---|---|---|---|---|---|---|
| Vehicle | `/api/v1/vehicles` CRUD | `wagenpulse_vehicle`, data `wagenpulse_vehicle` | vehicle ID or slug | destroy = archive (telemetry kept, ADR-0023) | none (VIN is sensitive-in-logs only) | acceptance-tested |
| Source (portal or upload) incl. collection schedule, request types, event policy, bound vehicles | `/api/v1/sources` CRUD | `wagenpulse_source` (nested `collection`, authoritative `vehicle_ids`) | source ID | destroy stops collection and deletes stored credentials; imported data kept | `password_wo`/`session_cookie_wo` + `credentials_wo_version`, or `password_ref` | acceptance-tested |
| Source authorization state | `status` object of `GET /api/v1/sources/{id}` | data `wagenpulse_source_status` | — | read-only runtime | — | acceptance-tested |
| Event rule (conditions, filters, debounce, cooldown) | `/api/v1/rules` CRUD | `wagenpulse_event_rule` | rule ID | destroy soft-deletes; history kept | none | acceptance-tested |
| Pulse/NATS adapter | `/api/v1/adapters` CRUD | `wagenpulse_pulse_adapter` | adapter ID | destroy refused while deliveries pending unless `abandon_pending_on_destroy` | `creds_wo` + `creds_wo_version`, or `creds_ref` | acceptance-tested |
| Route (event → adapter binding) | `/api/v1/routes` CRUD | `wagenpulse_route` | route ID | destroy stops new deliveries only | none | acceptance-tested |
| Retention settings | `/api/v1/settings/retention` GET/PUT | `wagenpulse_retention_settings` (singleton) | `retention` | destroy resets to defaults | none | acceptance-tested |
| System settings (login methods, session lifetimes, default event policies, stale threshold) | `/api/v1/settings/system` GET/PUT | `wagenpulse_system_settings` (singleton) | `system` | destroy resets to defaults; refuses root lockout | none | acceptance-tested |
| OIDC provider | `/api/v1/oidc-providers` CRUD | `wagenpulse_oidc_provider` | provider ID or slug | destroy unlinks identities | `client_secret_wo` + version, or `client_secret_ref` | acceptance-tested |
| User (role, status, local login enabled) | `/api/v1/users` CRUD | `wagenpulse_user` | user ID or username | destroy deletes user, revokes sessions/tokens; root not manageable | initial password write-only `password_wo` (optional) | acceptance-tested |
| API token | `/api/v1/tokens` | `wagenpulse_api_token` | token ID (secret not recoverable) | all arguments force replacement; destroy revokes; refuses to manage the provider's own credential | `token` sensitive, stored in state (documented) | acceptance-tested |
| Signal metadata | `/api/v1/signals` | data `wagenpulse_signal` | — | read-only | — | acceptance-tested |
| Server info / compatibility | `/api/v1/system/info` | data `wagenpulse_server_info` | — | read-only | — | acceptance-tested |

## Exclusions (justified)

| Not managed by Terraform | Reason |
|---|---|
| Telemetry, latest state, raw deliveries | Application-generated data, not configuration |
| Imports, collection runs, operations | Runtime processes; trigger them through the API/UI |
| Generated events, deliveries, attempts | Runtime outputs; replay/retry are operational actions |
| Upstream consent and portal data requests | Interactive and owned by VW's portal; cannot be automated legitimately |
| Dictionary versions | Shipped with the binary, or imported as data files through the UI/API (`POST /api/v1/uploads/dictionary`) |
| Root user and the bootstrap token | Created once on the host (`wagenpulse admin setup-token`); Terraform authenticates with a token minted by root |
| KEK, listeners, data directory, public URL | Deployment/bootstrap configuration (YAML/env), ADR-0004 |
| Sessions, UI preferences (theme, table density) | Ephemeral, per browser |
| Audit log | Append-only record |

## Ownership model

Nested configuration inside a resource is authoritative (for example `wagenpulse_source.vehicle_ids`); no separate
resource manages the same relationship, so plans cannot fight each other. Routes are separate resources because a
route is an entity with its own lifecycle (an adapter may receive from many rules and vice versa).

## Known limitations

- **Archived vehicles keep their slug.** Destroying a `wagenpulse_vehicle` archives it (ADR-0023), and slugs stay
  unique across archived vehicles. Re-creating the same configuration therefore fails with `409`. Use
  `terraform import wagenpulse_vehicle.x <slug>` to adopt and restore the archived vehicle. A true delete is the
  interactive purge, which API tokens cannot call.
- **Some secrets cannot be cleared:** a user's local password, and an adapter's stored `.creds`. Removing the `*_wo`
  argument keeps the stored value.
- **Root-only interactive actions are not available to Terraform:** non-expiring tokens and administrator users.
- **Rate limit:** API tokens are limited to 600 requests per minute (burst 120). The provider retries `429` responses,
  honouring `Retry-After`.
