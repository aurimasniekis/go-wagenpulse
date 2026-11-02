package adapters

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/netguard"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/metrics"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Factory opens a publisher for an adapter (replaced in tests).
type Factory func(a Adapter, creds []byte, onState func(bool, string)) (Publisher, error)

// ManagerOptions configure dispatching.
type ManagerOptions struct {
	Guard          *netguard.Guard
	InstallationID string
	Log            *slog.Logger
	Metrics        *metrics.Metrics
	Factory        Factory
	BaseBackoff    time.Duration // default 1s
	MaxBackoff     time.Duration // default 5m
	Poll           time.Duration // default 2s
	Concurrency    int           // in-flight publishes per adapter (default 1: FIFO per adapter)
}

// Manager runs one dispatcher per enabled adapter and reconciles them with configuration changes. A failing adapter
// only delays its own deliveries (ADR-0015).
type Manager struct {
	svc     *Service
	opts    ManagerOptions
	log     *slog.Logger
	mu      sync.Mutex
	workers map[string]*worker
	reload  chan struct{}
}

type worker struct {
	adapter Adapter
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
}

// NewManager returns a manager.
func NewManager(svc *Service, opts ManagerOptions) *Manager {
	if opts.Log == nil {
		opts.Log = obs.Discard()
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 5 * time.Minute
	}
	if opts.Poll <= 0 {
		opts.Poll = 2 * time.Second
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	m := &Manager{svc: svc, opts: opts, log: obs.Component(opts.Log, "dispatcher"), workers: map[string]*worker{}, reload: make(chan struct{}, 1)}
	if m.opts.Factory == nil {
		m.opts.Factory = m.pulseFactory
	}
	svc.Changed = m.Reload
	return m
}

func (m *Manager) pulseFactory(a Adapter, creds []byte, onState func(bool, string)) (Publisher, error) {
	source := a.Config.Source
	if source == "" {
		source = "wagenpulse/" + m.opts.InstallationID
	}
	log := m.log.With(obs.FieldAdapterID, a.ID)
	return ConnectPulse(PulseOptions{URL: a.Config.URL, Creds: creds, CAPEM: []byte(a.Config.CAPEM), Source: source, Name: "wagenpulse " + a.Name,
		Guard: m.opts.Guard, OnState: onState, OnAsyncError: func(err error) {
			class := classifyConnErr(err)
			log.Warn("nats asynchronous error", "error.class", class, obs.Err(err))
			_ = setStatus(context.Background(), m.svc.s, a.ID, "connected", Hint(class), class, false)
		}})
}

// ReleaseOrphanedLeases makes deliveries left "processing" by a crashed process due at once (see
// jobs.Runner.ReleaseOrphanedLeases). An attempt that reached JetStream before the crash is published again with the
// same Nats-Msg-Id and acknowledged as a duplicate inside the stream's dedupe window.
func (m *Manager) ReleaseOrphanedLeases(ctx context.Context) (int64, error) {
	if m.svc.s.Attached() {
		return 0, errors.New("adapters: an attached process must not release leases")
	}
	var n int64
	err := m.svc.s.W().TxNamed(ctx, "deliveries.release_orphans", func(tx *store.Tx) error {
		res, err := tx.SQL.ExecContext(ctx, `UPDATE deliveries SET lease_until = ?, updated_at = ? WHERE state = 'processing'`, tx.NowMicros()-1, tx.NowMicros())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if n > 0 {
		m.log.Warn("recovering deliveries interrupted by an unclean shutdown", slog.Int64("deliveries", n))
	}
	return n, err
}

// Reload asks the manager to reconcile workers with the configuration.
func (m *Manager) Reload() {
	select {
	case m.reload <- struct{}{}:
	default:
	}
}

// Wake prompts every worker to look for due deliveries.
func (m *Manager) Wake() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// Run reconciles until ctx is canceled, then stops every worker (in-flight publishes finish or time out).
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		if err := m.reconcile(ctx); err != nil && ctx.Err() == nil {
			m.log.WarnContext(ctx, "reconciling adapters failed", obs.Err(err))
		}
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-m.reload:
		case <-t.C:
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) error {
	as, err := m.svc.List(ctx)
	if err != nil {
		return err
	}
	want := map[string]Adapter{}
	for _, a := range as {
		if a.Enabled {
			want[a.ID] = a
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, w := range m.workers {
		if a, ok := want[id]; !ok || a.Revision != w.adapter.Revision {
			w.cancel()
			<-w.done
			delete(m.workers, id)
		}
	}
	for id, a := range want {
		if _, ok := m.workers[id]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		w := &worker{adapter: a, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
		m.workers[id] = w
		go func() {
			defer close(w.done)
			m.runWorker(wctx, w)
		}()
	}
	for id, a := range as {
		_ = id
		if !a.Enabled {
			_ = setStatus(ctx, m.svc.s, a.ID, "disabled", "", "", false)
		}
	}
	return nil
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, w := range m.workers {
		w.cancel()
		<-w.done
		delete(m.workers, id)
	}
}

func (m *Manager) runWorker(ctx context.Context, w *worker) {
	a := w.adapter
	log := m.log.With(obs.FieldAdapterID, a.ID)
	ctx = obs.WithID(ctx, obs.FieldAdapterID, a.ID)
	creds, err := m.svc.Creds(ctx, a.ID)
	if err != nil {
		_ = setStatus(ctx, m.svc.s, a.ID, "error", "credentials unavailable: "+truncate(err.Error(), 200), ClassCreds, false)
		m.opts.Metrics.SetAdapterConnected(a.ID, false)
		<-ctx.Done()
		return
	}
	_ = setStatus(ctx, m.svc.s, a.ID, "connecting", "", "", false)
	pub, err := m.opts.Factory(a, creds, func(connected bool, reason string) {
		m.opts.Metrics.SetAdapterConnected(a.ID, connected)
		if connected {
			_ = setStatus(context.WithoutCancel(ctx), m.svc.s, a.ID, "connected", "", "", true)
			select {
			case w.wake <- struct{}{}:
			default:
			}
		} else {
			_ = setStatus(context.WithoutCancel(ctx), m.svc.s, a.ID, "disconnected", Hint(reason), reason, false)
		}
	})
	if err != nil {
		class, _ := Classify(err)
		_ = setStatus(ctx, m.svc.s, a.ID, "error", "connect failed: "+truncate(err.Error(), 200), class, false)
		log.WarnContext(ctx, "adapter connection failed", obs.Err(err))
		<-ctx.Done()
		return
	}
	defer pub.Close()
	t := time.NewTicker(m.opts.Poll)
	defer t.Stop()
	for {
		n, err := m.dispatchDue(ctx, a, pub)
		if err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "dispatching failed", obs.Err(err))
		}
		if n > 0 && ctx.Err() == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-t.C:
		}
	}
}

type claimed struct {
	num         int64
	id          string
	msgID       string
	attempt     int64
	maxAttempts int64
	replaySeq   int64
	replayOf    string
	eventID     string
	eventType   string
	payload     string
	vehicleVIN  string
	ruleID      string
	createdAt   time.Time
}

// dispatchDue claims and publishes due deliveries of one adapter. It returns how many it processed.
func (m *Manager) dispatchDue(ctx context.Context, a Adapter, pub Publisher) (int, error) {
	if !pub.Connected() {
		return 0, nil // deliveries wait; no attempt is consumed while disconnected
	}
	timeout := time.Duration(a.Config.PublishTimeoutMS) * time.Millisecond
	var batch []claimed
	err := m.svc.s.W().TxNamed(ctx, "dispatch.claim", func(tx *store.Tx) error {
		batch = batch[:0]
		rows, err := tx.SQL.QueryContext(ctx, `SELECT d.num, d.id, d.msg_id, d.attempts, d.max_attempts, d.replay_seq, coalesce(d.replay_of, ''), e.id, e.type,
			e.payload_json, coalesce(v.vin, ''), coalesce(e.rule_id, ''), e.created_at
			FROM deliveries d JOIN events e ON e.num = d.event_num LEFT JOIN vehicles v ON v.num = e.vehicle_num
			WHERE d.adapter_id = ? AND ((d.state IN ('pending', 'retrying') AND d.next_attempt_at <= ?) OR (d.state = 'processing' AND d.lease_until < ?))
			ORDER BY d.num LIMIT ?`, a.ID, tx.NowMicros(), tx.NowMicros(), m.opts.Concurrency*10)
		if err != nil {
			return err
		}
		if err := func() error {
			defer rows.Close()
			for rows.Next() {
				var c claimed
				var created int64
				if err := rows.Scan(&c.num, &c.id, &c.msgID, &c.attempt, &c.maxAttempts, &c.replaySeq, &c.replayOf, &c.eventID, &c.eventType, &c.payload,
					&c.vehicleVIN, &c.ruleID, &created); err != nil {
					return err
				}
				c.createdAt = time.UnixMicro(created).UTC()
				batch = append(batch, c)
			}
			return rows.Err()
		}(); err != nil {
			return err
		}
		lease := tx.Now.Add(2*timeout + 5*time.Second).UnixMicro()
		for i := range batch {
			batch[i].attempt++
			if _, err := tx.SQL.ExecContext(ctx, `UPDATE deliveries SET state = 'processing', attempts = ?, lease_until = ?, updated_at = ? WHERE num = ?`,
				batch[i].attempt, lease, tx.NowMicros(), batch[i].num); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	for _, c := range batch {
		if ctx.Err() != nil {
			// Shutdown: release the lease so the next start retries without waiting.
			m.finish(context.WithoutCancel(ctx), a, c, nil, ctx.Err(), time.Now(), true)
			return 0, ctx.Err()
		}
		msg, err := buildMessage(a, c)
		start := time.Now()
		var ack *Ack
		if err == nil {
			pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
			ack, err = pub.Publish(pctx, msg)
			cancel()
		}
		m.finish(context.WithoutCancel(ctx), a, c, ack, err, start, false)
	}
	return len(batch), nil
}

// buildMessage maps an event to the adapter's subject and envelope (VIN only when the adapter opts in).
func buildMessage(a Adapter, c claimed) (Message, error) {
	var env events.Envelope
	if err := json.Unmarshal([]byte(c.payload), &env); err != nil {
		return Message{}, err
	}
	if a.Config.IncludeVIN {
		env.Vehicle.VIN = c.vehicleVIN
	}
	if c.replaySeq > 0 {
		env.Replay = &events.Replay{Seq: c.replaySeq, OfDelivery: c.replayOf, At: time.Now().UTC()}
	}
	body, err := json.Marshal(env)
	if err != nil {
		return Message{}, err
	}
	rule := "none"
	if c.ruleID != "" {
		rule = SubjectToken(c.ruleID)
	}
	severity := env.Severity
	if severity == "" {
		severity = "info"
	}
	subject := RenderSubject(a.Config.SubjectTemplate, map[string]string{"domain": a.Config.SubjectDomain, "event": SubjectToken(c.eventType),
		"vehicle": SubjectToken(env.Vehicle.Slug), "rule": rule, "severity": SubjectToken(severity)})
	headers := map[string]string{"wagenpulse-event-id": c.eventID, "wagenpulse-schema": "1"}
	if env.Backfill {
		headers["wagenpulse-backfill"] = "true"
	}
	if c.replaySeq > 0 {
		headers["wagenpulse-replay"] = "true"
	}
	return Message{MsgID: c.msgID, Subject: subject, Type: "wagenpulse." + c.eventType + ".v1", Time: env.ObservedAt, Body: body, Headers: headers}, nil
}

// finish records the outcome of an attempt.
func (m *Manager) finish(ctx context.Context, a Adapter, c claimed, ack *Ack, err error, start time.Time, interrupted bool) {
	now := time.Now()
	outcome, class, msg := "succeeded", "", ""
	state := "succeeded"
	var next time.Time
	permanent := false
	switch {
	case interrupted:
		outcome, state, next = "interrupted", "retrying", now
	case err != nil:
		class, permanent = Classify(err)
		msg = truncate(redactErr(err), 500)
		if h := Hint(class); h != "" {
			msg += " (" + h + ")"
		}
		if permanent || c.attempt >= c.maxAttempts {
			outcome, state = "dead", "dead"
		} else {
			outcome, state = "retry", "retrying"
			next = now.Add(backoff(m.opts.BaseBackoff, m.opts.MaxBackoff, int(c.attempt)))
		}
	}
	m.opts.Metrics.DeliveryAttempt(a.ID, outcome)
	if outcome == "succeeded" {
		m.opts.Metrics.DeliveryLatency(a.ID, now.Sub(c.createdAt))
	}
	subject := ""
	if msg, e := buildMessage(a, c); e == nil {
		subject = msg.Subject
	}
	werr := m.svc.s.W().TxNamed(ctx, "dispatch.finish", func(tx *store.Tx) error {
		var ackSeq, ackDup, ackStream, succeeded, nextAt any
		if ack != nil {
			ackSeq, ackDup, ackStream = int64(ack.Sequence), b2i(ack.Duplicate), ack.Stream //nolint:gosec // stream sequences fit int64
		}
		if state == "succeeded" {
			succeeded = tx.NowMicros()
		}
		nextAt = tx.NowMicros()
		if !next.IsZero() {
			nextAt = next.UnixMicro()
		}
		deadReason := ""
		if state == "dead" {
			deadReason = "max_attempts"
			if permanent {
				deadReason = "permanent_error"
			}
		}
		attempts := c.attempt
		if interrupted {
			attempts-- // an interrupted attempt does not consume the budget
		}
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE deliveries SET state = ?, attempts = ?, lease_until = NULL, next_attempt_at = ?, last_error_class = ?,
			last_error = ?, succeeded_at = coalesce(?, succeeded_at), ack_stream = coalesce(?, ack_stream), ack_seq = coalesce(?, ack_seq),
			ack_duplicate = coalesce(?, ack_duplicate), dead_reason = ?, subject = ?, updated_at = ? WHERE num = ? AND state = 'processing'`,
			state, attempts, nextAt, class, msg, succeeded, ackStream, ackSeq, ackDup, deadReason, subject, tx.NowMicros(), c.num); err != nil {
			return err
		}
		_, err := tx.SQL.ExecContext(ctx, `INSERT INTO delivery_attempts (delivery_num, attempt, started_at, finished_at, outcome, error_class, error_message,
			ack_seq, ack_duplicate, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, c.num, c.attempt, start.UnixMicro(),
			now.UnixMicro(), outcome, class, msg, ackSeq, ackDup, now.Sub(start).Milliseconds())
		return err
	})
	if werr != nil {
		m.log.ErrorContext(ctx, "recording a delivery attempt failed; the lease will expire and the delivery is retried", obs.Err(werr))
	}
	if err != nil && !interrupted {
		_ = setStatus(ctx, m.svc.s, a.ID, "connected", Hint(class), class, false)
	}
}

func redactErr(err error) string {
	s := err.Error()
	// NATS errors never contain credentials, but defensively strip anything that looks like a JWT or seed.
	for _, marker := range []string{"SUA", "eyJ"} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i] + "[redacted]"
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func backoff(base, maxDelay time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	d = min(d, maxDelay)
	var b [8]byte
	_, _ = rand.Read(b[:])
	half := d / 2
	return half + time.Duration(binary.LittleEndian.Uint64(b[:])%uint64(half+1)) //nolint:gosec // half ≥ 0
}

// TestResult reports a connection test.
type TestResult struct {
	OK         bool   `json:"ok"`
	Subject    string `json:"subject"`
	Stream     string `json:"stream,omitempty"`
	Sequence   uint64 `json:"sequence,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	ErrorClass string `json:"error_class,omitempty"`
	Error      string `json:"error,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Meaning    string `json:"meaning"`
}

// Test connects with the adapter's configuration and publishes one adapter.test event, waiting for the PubAck. A
// successful test proves JetStream stored the event; it does not prove any consumer processed it.
func (m *Manager) Test(ctx context.Context, actor audit.Actor, id string) (TestResult, error) {
	a, err := m.svc.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	res := TestResult{Meaning: "ok means the Pulse JetStream stream acknowledged storing the test event; consumer processing is not observable"}
	creds, err := m.svc.Creds(ctx, id)
	if err != nil {
		res.ErrorClass, res.Error = ClassCreds, err.Error()
		return res, m.recordTest(ctx, actor, a, res)
	}
	start := time.Now()
	connected := make(chan struct{}, 1)
	var lastReason string
	pub, err := m.opts.Factory(a, creds, func(ok bool, reason string) {
		if ok {
			select {
			case connected <- struct{}{}:
			default:
			}
		} else {
			lastReason = reason
		}
	})
	if err != nil {
		res.ErrorClass, _ = Classify(err)
		res.Error, res.Hint = truncate(redactErr(err), 300), Hint(res.ErrorClass)
		return res, m.recordTest(ctx, actor, a, res)
	}
	defer pub.Close()
	if !pub.Connected() {
		select {
		case <-connected:
		case <-time.After(10 * time.Second):
			res.ErrorClass = lastReason
			if res.ErrorClass == "" {
				res.ErrorClass = ClassUnavailable
			}
			res.Error, res.Hint = "could not connect within 10s", Hint(res.ErrorClass)
			res.DurationMS = time.Since(start).Milliseconds()
			return res, m.recordTest(ctx, actor, a, res)
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
	subject := RenderSubject(a.Config.SubjectTemplate, map[string]string{"domain": a.Config.SubjectDomain, "event": "adapter_test", "vehicle": "test",
		"rule": "none", "severity": "info"})
	res.Subject = subject
	body, _ := json.Marshal(map[string]any{"schema_version": events.SchemaVersion, "type": events.TypeAdapterTest, "adapter_id": a.ID,
		"created_at": time.Now().UTC()})
	pctx, cancel := context.WithTimeout(ctx, time.Duration(a.Config.PublishTimeoutMS)*time.Millisecond)
	defer cancel()
	ack, err := pub.Publish(pctx, Message{MsgID: "wtest" + randomSuffix(), Subject: subject, Type: "wagenpulse.adapter.test.v1", Time: time.Now(), Body: body})
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		res.ErrorClass, _ = Classify(err)
		res.Error, res.Hint = truncate(redactErr(err), 300), Hint(res.ErrorClass)
	} else {
		res.OK, res.Stream, res.Sequence = true, ack.Stream, ack.Sequence
	}
	return res, m.recordTest(ctx, actor, a, res)
}

func randomSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strings.ToLower(strings.TrimRight(base32(b[:]), "="))
}

func base32(b []byte) string {
	const enc = "abcdefghijklmnopqrstuvwxyz234567"
	var out strings.Builder
	for _, x := range b {
		out.WriteByte(enc[x%32])
	}
	return out.String()
}

func (m *Manager) recordTest(ctx context.Context, actor audit.Actor, a Adapter, res TestResult) error {
	result := "ok"
	if !res.OK {
		result = res.ErrorClass
	}
	return m.svc.s.W().TxNamed(ctx, "adapters.test", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO adapter_status (adapter_id, state, last_test_at, last_test_result, updated_at) VALUES (?, 'connecting', ?, ?, ?)
			ON CONFLICT (adapter_id) DO UPDATE SET last_test_at = excluded.last_test_at, last_test_result = excluded.last_test_result, updated_at = excluded.updated_at`,
			a.ID, tx.NowMicros(), result, tx.NowMicros()); err != nil {
			return err
		}
		outcome := audit.OutcomeSuccess
		if !res.OK {
			outcome = audit.OutcomeFailure
		}
		_, err := m.svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "adapter.tested", Target: audit.Target{Type: "adapter", ID: a.ID},
			Outcome: outcome, ReasonCode: res.ErrorClass})
		return err
	})
}

// UpdateQueueMetrics exports queue gauges.
func (m *Manager) UpdateQueueMetrics(ctx context.Context) error {
	rows, err := m.svc.s.ReadDB().QueryContext(ctx, `SELECT adapter_id, state, count(*) FROM deliveries WHERE adapter_id IS NOT NULL AND state != 'succeeded'
		GROUP BY adapter_id, state`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a, s string
		var n int64
		if err := rows.Scan(&a, &s, &n); err != nil {
			return err
		}
		m.opts.Metrics.SetDeliveryQueue(a, s, n)
	}
	return rows.Err()
}

var _ = sql.ErrNoRows
var _ = errors.New
