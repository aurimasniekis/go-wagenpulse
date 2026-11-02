# Storage design

See ADR-0002 (SQLite), ADR-0003 (identifiers), ADR-0008 (observation model), ADR-0009 (latest state), ADR-0024
(retention), ADR-0025 (backup).

## Workload assumptions

| Quantity | Assumption |
|---|---|
| Vehicles | 1–20 (design target), tested to 50 |
| Deliveries | one per vehicle per ≥15 min (portal cadence), plus occasional uploads |
| Values per delivery | 60–1100 (flat PHEV layout ≈ 64; "All Data" MEB ≈ 1000) |
| New observations | most values keep their capture timestamp between deliveries and deduplicate; realistic < 50k / vehicle / day, worst case ≈ 100k |
| Reads | interactive dashboards (latest state, 1 h–1 y charts), CSV exports ≤ 100k rows |
| Retention | observations 2 years by default |

At 20 vehicles × 50k/day × 730 days ≈ 730M rows worst-case-realistic — beyond comfortable SQLite size; with typical
deduplication (values change far less often than every delivery) the expected figure is 10–50M rows. Operators with
large fleets should shorten observation retention; rollups keep long-range charts available.

## Layout

- `data_dir/wagenpulse.db` (+ `-wal`, `-shm`), mode 0600, exclusive `wagenpulse.lock`.
- `data_dir/blobs/sha256/<2>/<64>` raw deliveries (content addressed, 0600).
- `data_dir/snapshots/` pre-migration snapshots.
- `data_dir/backups/` default target of `wagenpulse backup create`.

## Telemetry tables

```
signals(num INTEGER PK, key TEXT UNIQUE, name, description, unit_raw, unit, data_type_raw, value_kind, clusters_json,
        origin, dictionary_version_id, first_seen_at, last_seen_at)
observations(vehicle_num, signal_num, observed_at, value_hash, received_at, num REAL, text TEXT, quality, unit,
             raw TEXT, delivery_num, time_basis) PRIMARY KEY (vehicle_num, signal_num, observed_at, value_hash)
             WITHOUT ROWID
latest_state(vehicle_num, signal_num, observed_at, received_at, num, text, quality, unit, delivery_num)
             PRIMARY KEY (vehicle_num, signal_num) WITHOUT ROWID
observation_rollups(vehicle_num, signal_num, bucket_start, n, min, max, sum, last_num, last_at)
             PRIMARY KEY (vehicle_num, signal_num, bucket_start) WITHOUT ROWID       -- hourly buckets
```

The observations primary key is the clustered index, so a chart query for one vehicle and signal over a range is a
single B-tree range scan. Fleet-wide "latest" views read `latest_state` only.

## Query strategy

- Range ≤ 7 days and ≤ `max_points`: raw observations.
- Otherwise: buckets of `ceil(range / max_points)` aligned to the hour or larger, computed from `observation_rollups`
  when the bucket is ≥ 1 h, else `GROUP BY observed_at / bucket` over raw rows.
- Empty buckets are returned as gaps (`null`) and never interpolated. Text/enum signals return change points only.
- Hard limits: range ≤ 366 days, `max_points` ≤ 5000, export ≤ 100k rows.

## Writes

Each delivery is normalized in memory (bounded by the archive limits), then written in transactions of at most
`ingest.batch_size` observations: insert-or-ignore observations, upsert latest state with the precedence rule,
recompute touched rollup buckets, evaluate rules, insert events and deliveries. Network I/O never happens inside a
transaction.

## Performance validation

`make bench` runs `internal/observations` benchmarks (ingest throughput, chart queries on a synthetic 1-year dataset).
Results are recorded below only after they have been measured.

| Benchmark | Result | Date / machine |
|---|---|---|
| (pending M10) | | |
