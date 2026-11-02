# Upgrades and key rotation

## Upgrading

1. Take a backup and verify it: `backup create`, then `backup verify`.
2. Replace the binary or image and restart.
3. Pending database migrations run at start. Before migrating, WagenPulse writes a snapshot to
   `<data_dir>/backups/pre-migrate-<from>-<to>-<time>.db` and keeps the newest 3.
4. If a migration fails, the database stays at the last successful version and the error names the snapshot.

**Downgrades are not supported.** An older binary refuses a newer schema. To go back, restore the backup taken before
the upgrade with the old binary.

Use `wagenpulse migrate` to apply migrations without starting the server; the server must be stopped.

## Rotating keys

| What | Command | Effect |
|---|---|---|
| A data key (`field` seals secrets, `token` MACs sessions and API tokens) | `wagenpulse keys rotate --purpose field` | A new version becomes active and `field` secrets are resealed. The old version stays verify/decrypt-only until `keys retire`. For `token`, existing sessions and API tokens keep working until the old version is retired; retiring it signs everyone out and invalidates API tokens created before the rotation |
| The KEK | `wagenpulse keys init --out new.kek`, then `wagenpulse keys rewrap --new-kek new.kek` | Every keyring key is rewrapped under the new KEK in one transaction; afterwards only the new KEK opens the database |
| Status | `wagenpulse keys status` | KEK ID, purposes and versions; works while the server runs |

All of these except `keys status` need the server stopped, because they change keys the server holds in memory.

After a KEK rewrap:

- point `kek.file` at the new file and start the server;
- keep the old KEK until the server starts and your existing backups have been replaced, because older backups still
  need it;
- then destroy the old KEK.
