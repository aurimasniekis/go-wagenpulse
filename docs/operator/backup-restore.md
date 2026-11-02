# Backup, restore and retention

## What to back up

| What | How | Notes |
|---|---|---|
| Database and raw deliveries | `wagenpulse backup create --out DIR` | Online: safe while the server runs |
| KEK | Copy the KEK file once, offline, **separately** from data backups | Without it, a backup's stored secrets cannot be decrypted |
| Configuration | Your config file / `.env` | Contains no secrets |

## Creating a backup

```sh
wagenpulse backup create --out /backups/wagenpulse-$(date +%Y%m%dT%H%M)
```

The command:

1. writes a consistent copy of the database (SQLite `VACUUM INTO`);
2. copies the raw delivery files that copy references;
3. writes `manifest.json` with the SHA-256 and size of every file, the schema version and the installation ID.

The target must not exist yet, and must not be inside the data directory. Put it on separate storage.

A raw delivery that retention removed while the backup ran is listed under `missing_blobs`. Its observations are
intact; only reprocessing that one file is impossible.

In containers, mount a backup volume and run `docker compose exec wagenpulse /usr/local/bin/wagenpulse backup create
--out /backups/...`.

## Verifying

```sh
wagenpulse backup verify --dir /backups/wagenpulse-20261102T1200   # exit 0 = OK, 3 = verification failed
```

It checks:

- the manifest format;
- each file's size and SHA-256, and that no unlisted files are present;
- that each blob's name equals its content hash;
- SQLite `integrity_check`;
- the schema version.

Verify backups regularly, and always before relying on one.

## Restoring

1. Stop the server.
2. Point the configuration at an **empty** data directory (or a new path) and the **same KEK** as the backed-up
   installation.
3. Run:

   ```sh
   wagenpulse restore --from /backups/wagenpulse-20261102T1200
   ```

   `restore` verifies the backup first and refuses a schema newer than the binary. It takes the data-directory lock,
   so it fails while a server runs, and it never writes into a non-empty directory.
4. Start the server. If the backup is from an older version, pending migrations run on start, after a
   pre-migration snapshot.

Development installations keep their generated KEK at `<data_dir>/dev-kek`, which is not part of the backup. To
restore a development backup, set `kek.file` to a copy of that file.

## Retention

Configure retention in **Settings → Retention** or with Terraform. `0` keeps data forever.

| Data | Default |
|---|---|
| Observations (rollups are kept as long as observations) | 730 days |
| Raw delivery files (needed to reprocess) | 90 days |
| Events and deliveries | 180 days |
| Audit log | 730 days (minimum 90) |
| Finished jobs and imports | 90 days |

Retention runs every 6 hours, or on demand with **Run now**. Deletions cannot be undone except from a backup.
Purging the audit log keeps the hash chain verifiable by recording an anchor.
