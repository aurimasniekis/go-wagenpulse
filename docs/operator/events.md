# Rules, routes and delivery

**Rules** turn observations into events. Each event is stored. **Routes** decide which adapters receive which events.
An event with no matching route is stored but not delivered.

## Rules

| Type | Fires when | Emits |
|---|---|---|
| Threshold | A numeric value goes above or below a limit. It re-arms after returning past the hysteresis band | `threshold.crossed`, optionally `threshold.cleared` |
| Change | A value changes (numbers: by at least the deadband) | `signal.changed` |
| Transition | A text or boolean value moves from one of *from* to one of *to* | `state.transition` |
| Stale data | No new observation arrived for a period (checked every minute, not only on import) | `data.stale`, optionally `data.resumed` |

Other behaviour:

- *Debounce* requires the condition on N consecutive observations. *Cooldown* suppresses repeats per vehicle and
  signal, measured in observation time.
- Rules evaluate in observation-time order. Late observations (older than the newest one already evaluated) are stored
  but never fire; the import reports them as `late_skipped`.
- Editing a rule creates a new immutable revision and resets its state. Each event records the revision that produced
  it.
- **Preview** runs a rule over stored history without writing or delivering anything.

## Pulse adapters

An adapter publishes to NATS JetStream as Pulse expects:

- **Subject**: rendered from a template, `evt.{domain}.vehicle.{event}.{vehicle}` by default (placeholders `{domain}`,
  `{event}`, `{vehicle}`, `{rule}`, `{severity}`). It is validated against Pulse's subject rules when you save.
- **Headers**: CloudEvents binary-mode headers (`ce-type: wagenpulse.<event>.v1`, `ce-source`, `ce-time`) and a
  deterministic `Nats-Msg-Id`.
- **Body**: the JSON envelope v1, which identifies vehicles by ID and slug. The VIN is excluded unless *Include VIN*
  is enabled.
- **Credentials**: a NATS `.creds` file, stored encrypted or referenced externally. TLS is required unless you
  explicitly allow plain `nats://`.

**Test connection** publishes `evt.<domain>.adapter.test.<id>` and reports whether JetStream stored it. A **timeout**
usually means the credentials lack a publish grant for the subject; Pulse reports a missing grant as a timeout.

## Delivery guarantee

Delivery is **at-least-once**:

1. Events and their deliveries are written in the same transaction as the observations that caused them (an outbox).
2. Each adapter has its own worker, so a failing adapter never blocks the others or ingestion.
3. Failures retry with exponential backoff up to *max attempts*; the delivery is then marked `dead`. You can still
   retry it manually.
4. A crash mid-publish is recovered at the next start. A message that already reached JetStream is acknowledged as a
   duplicate thanks to `Nats-Msg-Id` (inside the stream's 2-minute dedupe window), so it is not stored twice.

"Delivered" means JetStream stored the message. Pulse does not report whether a consumer processed it.

## Retry and replay

- **Retry now** on a `dead` or `retrying` delivery schedules an immediate attempt.
- **Replay** re-sends stored events from a time range to one adapter as new deliveries with a new `Nats-Msg-Id` and a
  replay marker. It counts the matching events first and asks for confirmation.
