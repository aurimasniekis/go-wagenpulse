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
- `data_dir/backups/` pre-migration snapshots (`VACUUM INTO`, newest 3 kept).
- Backups (`wagenpulse backup create --out DIR`) are written outside the data directory; targets inside it are refused
  (ADR-0025).

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

`make bench` runs the `internal/telemetry` benchmarks. The dataset is synthetic: one vehicle, 16 numeric signals,
one year of deliveries every 15 minutes with three capture times each. That is 1,681,920 observations, written
through the production `Writer` (dedupe, latest state, hourly rollups) in about 2 min 20 s, roughly 12k
observations/s including rollup upkeep.

Measured 2026-11-02 on an Apple M1 Max (arm64, macOS, SSD), `go test -bench -benchtime 10x`:

| Benchmark | Result |
|---|---|
| Ingest a new 48-value delivery (fresh store) | 3.1 ms/delivery, ≈ 15,600 observations/s |
| Re-import an identical delivery (dedupe path) | 0.42 ms/delivery, ≈ 114,000 observations/s |
| Chart, 24 h (raw, 288 points) | 2.4 ms |
| Chart, 7 days (raw, 672 points) | 4.1 ms |
| Chart, 30 days (hourly rollups, 720 points) | 2.9 ms |
| Chart, 365 days (rollups, 730 daily points) | 27 ms |
| Latest state, 16 signals | 0.15 ms |
| Export, 30 days × 4 signals (34,560 rows) | 116 ms, ≈ 300,000 rows/s |

The 30-day and 365-day charts first measured about 1 s each. A correlated subquery for an unused "last value" made
the rollup query quadratic; removing it gave the figures above. `TestRollupBucketsMatchRawAggregates` checks that
rollup buckets equal aggregates computed from the raw observations, gaps included.

At the planned load (20 vehicles, one delivery per 15 minutes each), ingest uses well under 1% of one writer.
Interactive charts stay below 30 ms for a full year of one signal.
