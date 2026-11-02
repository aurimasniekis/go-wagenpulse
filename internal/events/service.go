package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Service queries events and deliveries and performs retries and replays.
type Service struct {
	s     *store.Store
	audit *audit.Writer
	// Wake notifies dispatchers that deliveries are due.
	Wake func()
}

// NewService returns a service.
func NewService(s *store.Store, a *audit.Writer) *Service { return &Service{s: s, audit: a} }

// Event is the API view of an event.
type Event struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	RuleID        string          `json:"rule_id,omitempty"`
	RuleRevision  int64           `json:"rule_revision,omitempty"`
	VehicleID     string          `json:"vehicle_id,omitempty"`
	SignalKey     string          `json:"signal_key,omitempty"`
	ObservedAt    time.Time       `json:"observed_at"`
	CreatedAt     time.Time       `json:"created_at"`
	Backfill      bool            `json:"backfill"`
	ImportID      string          `json:"import_id,omitempty"`
	CorrelationID string          `json:"correlation_id"`
	Deliveries    DeliverySummary `json:"deliveries"`
	Payload       *Envelope       `json:"payload,omitempty" doc:"Envelope as stored (the VIN is never stored; adapters that opt in add it at publish time)."`
	num           int64
}

// DeliverySummary counts an event's deliveries by state.
type DeliverySummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Pending   int `json:"pending"`
	Retrying  int `json:"retrying"`
	Dead      int `json:"dead"`
}

// EventFilter selects events (newest first).
type EventFilter struct {
	VehicleNum  int64
	VehicleNums []int64
	RuleID      string
	Type        string
	ImportID    string
	From, To    time.Time
	Before      int64
	Limit       int
}

const eventCols = `e.num, e.id, e.type, coalesce(e.rule_id, ''), coalesce(e.rule_revision, 0), coalesce(v.id, ''), coalesce(s.key, ''), e.observed_at,
	e.created_at, e.backfill, coalesce(e.import_id, ''), e.correlation_id,
	(SELECT count(*) FROM deliveries d WHERE d.event_num = e.num),
	(SELECT count(*) FROM deliveries d WHERE d.event_num = e.num AND d.state = 'succeeded'),
	(SELECT count(*) FROM deliveries d WHERE d.event_num = e.num AND d.state IN ('pending', 'processing')),
	(SELECT count(*) FROM deliveries d WHERE d.event_num = e.num AND d.state = 'retrying'),
	(SELECT count(*) FROM deliveries d WHERE d.event_num = e.num AND d.state = 'dead')`

const eventFrom = ` FROM events e LEFT JOIN vehicles v ON v.num = e.vehicle_num LEFT JOIN signals s ON s.num = e.signal_num`

func scanEvent(r interface{ Scan(...any) error }, extra ...any) (Event, error) {
	var e Event
	var at, created int64
	var backfill int
	if err := r.Scan(append([]any{&e.num, &e.ID, &e.Type, &e.RuleID, &e.RuleRevision, &e.VehicleID, &e.SignalKey, &at, &created, &backfill, &e.ImportID,
		&e.CorrelationID, &e.Deliveries.Total, &e.Deliveries.Succeeded, &e.Deliveries.Pending, &e.Deliveries.Retrying, &e.Deliveries.Dead}, extra...)...); err != nil {
		return e, err
	}
	e.ObservedAt, e.CreatedAt, e.Backfill = time.UnixMicro(at).UTC(), time.UnixMicro(created).UTC(), backfill == 1
	return e, nil
}

// Events lists events.
func (svc *Service) Events(ctx context.Context, f EventFilter) ([]Event, int64, error) {
	q := `SELECT ` + eventCols + eventFrom + ` WHERE 1 = 1`
	var args []any
	if f.Before > 0 {
		q += ` AND e.num < ?`
		args = append(args, f.Before)
	}
	if f.VehicleNum > 0 {
		q += ` AND e.vehicle_num = ?`
		args = append(args, f.VehicleNum)
	}
	if len(f.VehicleNums) > 0 {
		q += ` AND e.vehicle_num IN (` + placeholders(len(f.VehicleNums)) + `)`
		for _, n := range f.VehicleNums {
			args = append(args, n)
		}
	}
	if f.RuleID != "" {
		q += ` AND e.rule_id = ?`
		args = append(args, f.RuleID)
	}
	if f.Type != "" {
		q += ` AND e.type = ?`
		args = append(args, f.Type)
	}
	if f.ImportID != "" {
		q += ` AND e.import_id = ?`
		args = append(args, f.ImportID)
	}
	if !f.From.IsZero() {
		q += ` AND e.observed_at >= ?`
		args = append(args, f.From.UnixMicro())
	}
	if !f.To.IsZero() {
		q += ` AND e.observed_at < ?`
		args = append(args, f.To.UnixMicro())
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q += ` ORDER BY e.num DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	var next int64
	if len(out) == f.Limit {
		next = out[len(out)-1].num
	}
	return out, next, rows.Err()
}

// ErrNotFound is returned for unknown events or deliveries.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "not found")

// Get returns an event with its payload.
func (svc *Service) Get(ctx context.Context, id string) (Event, int64, error) {
	var payload string
	e, err := scanEvent(svc.s.ReadDB().QueryRowContext(ctx, `SELECT `+eventCols+`, e.payload_json, coalesce(e.vehicle_num, 0)`+eventFrom+` WHERE e.id = ?`, id),
		&payload, new(int64))
	if errors.Is(err, sql.ErrNoRows) {
		return e, 0, ErrNotFound
	}
	if err != nil {
		return e, 0, err
	}
	var env Envelope
	if json.Unmarshal([]byte(payload), &env) == nil {
		env.Vehicle.VIN = ""
		e.Payload = &env
	}
	var vnum int64
	_ = svc.s.ReadDB().QueryRowContext(ctx, `SELECT coalesce(vehicle_num, 0) FROM events WHERE id = ?`, id).Scan(&vnum)
	return e, vnum, nil
}

// Delivery is the API view of a delivery.
type Delivery struct {
	ID             string     `json:"id"`
	EventID        string     `json:"event_id"`
	EventType      string     `json:"event_type"`
	VehicleID      string     `json:"vehicle_id,omitempty"`
	AdapterID      string     `json:"adapter_id,omitempty"`
	AdapterName    string     `json:"adapter_name"`
	RouteID        string     `json:"route_id,omitempty"`
	ReplaySeq      int64      `json:"replay_seq"`
	ReplayOf       string     `json:"replay_of,omitempty"`
	MsgID          string     `json:"msg_id" doc:"Nats-Msg-Id; deterministic per (event, adapter, replay sequence)."`
	Subject        string     `json:"subject,omitempty"`
	State          string     `json:"state" enum:"pending,processing,succeeded,retrying,dead"`
	Attempts       int64      `json:"attempts"`
	MaxAttempts    int64      `json:"max_attempts"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
	LastErrorClass string     `json:"last_error_class,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	SucceededAt    *time.Time `json:"succeeded_at,omitempty" doc:"When the destination acknowledged storage (JetStream PubAck); not consumer processing."`
	AckStream      string     `json:"ack_stream,omitempty"`
	AckSeq         *int64     `json:"ack_seq,omitempty"`
	AckDuplicate   bool       `json:"ack_duplicate"`
	DeadReason     string     `json:"dead_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Attempt        []Attempt  `json:"attempt_history,omitempty"`
	num            int64
}

// Attempt is one delivery attempt.
type Attempt struct {
	Attempt      int64     `json:"attempt"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	Outcome      string    `json:"outcome" enum:"succeeded,retry,dead,interrupted"`
	ErrorClass   string    `json:"error_class,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	AckSeq       *int64    `json:"ack_seq,omitempty"`
	AckDuplicate bool      `json:"ack_duplicate"`
	DurationMS   int64     `json:"duration_ms"`
}

const deliveryCols = `d.num, d.id, e.id, e.type, coalesce(v.id, ''), coalesce(d.adapter_id, ''), d.adapter_name, coalesce(d.route_id, ''), d.replay_seq,
	coalesce(d.replay_of, ''), d.msg_id, d.subject, d.state, d.attempts, d.max_attempts, d.next_attempt_at, d.last_error_class, d.last_error,
	d.succeeded_at, coalesce(d.ack_stream, ''), d.ack_seq, coalesce(d.ack_duplicate, 0), d.dead_reason, d.created_at, d.updated_at`

const deliveryFrom = ` FROM deliveries d JOIN events e ON e.num = d.event_num LEFT JOIN vehicles v ON v.num = e.vehicle_num`

func scanDelivery(r interface{ Scan(...any) error }) (Delivery, error) {
	var d Delivery
	var next, succeeded, ackSeq sql.NullInt64
	var dup int64
	var created, updated int64
	if err := r.Scan(&d.num, &d.ID, &d.EventID, &d.EventType, &d.VehicleID, &d.AdapterID, &d.AdapterName, &d.RouteID, &d.ReplaySeq, &d.ReplayOf,
		&d.MsgID, &d.Subject, &d.State, &d.Attempts, &d.MaxAttempts, &next, &d.LastErrorClass, &d.LastError, &succeeded, &d.AckStream, &ackSeq, &dup,
		&d.DeadReason, &created, &updated); err != nil {
		return d, err
	}
	if next.Valid && (d.State == "pending" || d.State == "retrying") {
		t := time.UnixMicro(next.Int64).UTC()
		d.NextAttemptAt = &t
	}
	if succeeded.Valid {
		t := time.UnixMicro(succeeded.Int64).UTC()
		d.SucceededAt = &t
	}
	if ackSeq.Valid {
		v := ackSeq.Int64
		d.AckSeq = &v
	}
	d.AckDuplicate = dup == 1
	d.CreatedAt, d.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	return d, nil
}

// DeliveryFilter selects deliveries (newest first).
type DeliveryFilter struct {
	AdapterID   string
	State       string
	EventID     string
	VehicleNums []int64
	Before      int64
	Limit       int
}

// Deliveries lists deliveries.
func (svc *Service) Deliveries(ctx context.Context, f DeliveryFilter) ([]Delivery, int64, error) {
	q := `SELECT ` + deliveryCols + deliveryFrom + ` WHERE 1 = 1`
	var args []any
	if f.Before > 0 {
		q += ` AND d.num < ?`
		args = append(args, f.Before)
	}
	if f.AdapterID != "" {
		q += ` AND d.adapter_id = ?`
		args = append(args, f.AdapterID)
	}
	if f.State != "" {
		q += ` AND d.state = ?`
		args = append(args, f.State)
	}
	if f.EventID != "" {
		q += ` AND e.id = ?`
		args = append(args, f.EventID)
	}
	if len(f.VehicleNums) > 0 {
		q += ` AND e.vehicle_num IN (` + placeholders(len(f.VehicleNums)) + `)`
		for _, n := range f.VehicleNums {
			args = append(args, n)
		}
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q += ` ORDER BY d.num DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	var next int64
	if len(out) == f.Limit {
		next = out[len(out)-1].num
	}
	return out, next, rows.Err()
}

// Delivery returns one delivery with its attempt history.
func (svc *Service) Delivery(ctx context.Context, id string) (Delivery, error) {
	d, err := scanDelivery(svc.s.ReadDB().QueryRowContext(ctx, `SELECT `+deliveryCols+deliveryFrom+` WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT attempt, started_at, finished_at, outcome, error_class, error_message, ack_seq,
		coalesce(ack_duplicate, 0), duration_ms FROM delivery_attempts WHERE delivery_num = ? ORDER BY attempt`, d.num)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	d.Attempt = []Attempt{}
	for rows.Next() {
		var a Attempt
		var s, f int64
		var seq sql.NullInt64
		var dup int64
		if err := rows.Scan(&a.Attempt, &s, &f, &a.Outcome, &a.ErrorClass, &a.ErrorMessage, &seq, &dup, &a.DurationMS); err != nil {
			return d, err
		}
		a.StartedAt, a.FinishedAt, a.AckDuplicate = time.UnixMicro(s).UTC(), time.UnixMicro(f).UTC(), dup == 1
		if seq.Valid {
			v := seq.Int64
			a.AckSeq = &v
		}
		d.Attempt = append(d.Attempt, a)
	}
	return d, rows.Err()
}

// RetryBudget is the number of attempts a manual retry grants.
const RetryBudget = 5

// Retry makes a dead or retrying delivery due now with a fresh attempt budget. The Nats-Msg-Id is unchanged, so a
// copy the destination already stored within its dedupe window is acknowledged as a duplicate rather than stored twice.
func (svc *Service) Retry(ctx context.Context, actor audit.Actor, id string) (Delivery, error) {
	err := svc.s.W().TxNamed(ctx, "events.retry", func(tx *store.Tx) error {
		r, err := tx.SQL.ExecContext(ctx, `UPDATE deliveries SET state = 'pending', next_attempt_at = ?, max_attempts = attempts + ?, dead_reason = '',
			updated_at = ? WHERE id = ? AND state IN ('dead', 'retrying') AND adapter_id IS NOT NULL`,
			tx.NowMicros(), RetryBudget, tx.NowMicros(), tx.NowMicros(), id)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			var state string
			if err := tx.SQL.QueryRowContext(ctx, `SELECT state FROM deliveries WHERE id = ?`, id).Scan(&state); errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return apperr.New(apperr.CodeConflict, "only dead or retrying deliveries to an existing adapter can be retried")
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "delivery.retried", Target: audit.Target{Type: "delivery", ID: id}})
		return err
	})
	if err != nil {
		return Delivery{}, err
	}
	svc.wake()
	return svc.Delivery(ctx, id)
}

func (svc *Service) wake() {
	if svc.Wake != nil {
		svc.Wake()
	}
}

// ReplayRequest selects events to deliver again to one adapter (ADR-0017).
type ReplayRequest struct {
	AdapterID   string
	From, To    time.Time
	Types       []string
	VehicleNums []int64
	RuleID      string
	Confirm     bool
}

// ReplayResult reports a preview or an executed replay.
type ReplayResult struct {
	Events    int  `json:"events"`
	Created   int  `json:"deliveries_created"`
	Confirmed bool `json:"confirmed"`
	Limit     int  `json:"limit"`
}

// MaxReplay bounds one replay.
const MaxReplay = 10000

// Replay previews (Confirm=false) or creates new deliveries for selected events to one adapter. Replayed deliveries
// carry a new replay sequence, hence a new Nats-Msg-Id, so the destination stores them again; consumers deduplicate
// on event_id (ADR-0016).
func (svc *Service) Replay(ctx context.Context, actor audit.Actor, req ReplayRequest) (ReplayResult, error) {
	if !req.To.After(req.From) || req.To.Sub(req.From) > 366*24*time.Hour {
		return ReplayResult{}, apperr.New(apperr.CodeInvalid, "the time range must be positive and at most 366 days")
	}
	for _, t := range req.Types {
		if !ValidType(t) {
			return ReplayResult{}, apperr.New(apperr.CodeInvalid, "unknown event type %q", t)
		}
	}
	var adapterName, cfg string
	var enabled int
	if err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT name, enabled, config_json FROM adapters WHERE id = ?`, req.AdapterID).Scan(&adapterName, &enabled, &cfg); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReplayResult{}, apperr.New(apperr.CodeNotFound, "adapter not found")
		}
		return ReplayResult{}, err
	}
	if enabled != 1 {
		return ReplayResult{}, apperr.New(apperr.CodeConflict, "the adapter is disabled")
	}
	q := `SELECT e.num, e.id FROM events e WHERE e.observed_at >= ? AND e.observed_at < ?`
	args := []any{req.From.UnixMicro(), req.To.UnixMicro()}
	if len(req.Types) > 0 {
		q += ` AND e.type IN (` + placeholders(len(req.Types)) + `)`
		for _, t := range req.Types {
			args = append(args, t)
		}
	}
	if len(req.VehicleNums) > 0 {
		q += ` AND e.vehicle_num IN (` + placeholders(len(req.VehicleNums)) + `)`
		for _, n := range req.VehicleNums {
			args = append(args, n)
		}
	}
	if req.RuleID != "" {
		q += ` AND e.rule_id = ?`
		args = append(args, req.RuleID)
	}
	q += ` ORDER BY e.num LIMIT ?`
	args = append(args, MaxReplay+1)
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return ReplayResult{}, err
	}
	type ev struct {
		num int64
		id  string
	}
	var evs []ev
	if err := func() error {
		defer rows.Close()
		for rows.Next() {
			var e ev
			if err := rows.Scan(&e.num, &e.id); err != nil {
				return err
			}
			evs = append(evs, e)
		}
		return rows.Err()
	}(); err != nil {
		return ReplayResult{}, err
	}
	res := ReplayResult{Events: len(evs), Limit: MaxReplay}
	if len(evs) > MaxReplay {
		return res, apperr.New(apperr.CodeInvalid, "the selection has more than %d events; narrow it", MaxReplay)
	}
	if !req.Confirm || len(evs) == 0 {
		return res, nil
	}
	var c struct {
		MaxAttempts int `json:"max_attempts"`
	}
	_ = json.Unmarshal([]byte(cfg), &c)
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 12
	}
	for start := 0; start < len(evs); start += 500 {
		chunk := evs[start:min(start+500, len(evs))]
		err := svc.s.W().TxNamed(ctx, "events.replay", func(tx *store.Tx) error {
			for _, e := range chunk {
				var seq int64
				var orig sql.NullString
				if err := tx.SQL.QueryRowContext(ctx, `SELECT coalesce(max(replay_seq), 0) + 1, (SELECT id FROM deliveries WHERE event_num = ? AND adapter_id = ?
					AND replay_seq = 0) FROM deliveries WHERE event_num = ? AND adapter_id = ?`, e.num, req.AdapterID, e.num, req.AdapterID).Scan(&seq, &orig); err != nil {
					return err
				}
				if err := insertDelivery(ctx, tx, e.num, e.id, req.AdapterID, adapterName, "", seq, orig.String, c.MaxAttempts); err != nil {
					return err
				}
				res.Created++
			}
			return nil
		})
		if err != nil {
			return res, err
		}
	}
	res.Confirmed = true
	err = svc.s.W().TxNamed(ctx, "events.replay_audit", func(tx *store.Tx) error {
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "delivery.replayed", Target: audit.Target{Type: "adapter", ID: req.AdapterID},
			Details: map[string]any{"events": res.Events, "from": req.From.UTC().Format(time.RFC3339), "to": req.To.UTC().Format(time.RFC3339),
				"types": strings.Join(req.Types, ","), "rule_id": req.RuleID}})
		return err
	})
	svc.wake()
	return res, err
}

// QueueStats returns deliveries by adapter and state (metrics, dashboards).
func (svc *Service) QueueStats(ctx context.Context) (map[string]map[string]int64, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT coalesce(adapter_id, ''), state, count(*) FROM deliveries
		WHERE state != 'succeeded' OR succeeded_at > ? GROUP BY adapter_id, state`, time.Now().Add(-24*time.Hour).UnixMicro())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int64{}
	for rows.Next() {
		var a, s string
		var n int64
		if err := rows.Scan(&a, &s, &n); err != nil {
			return nil, err
		}
		if out[a] == nil {
			out[a] = map[string]int64{}
		}
		out[a][s] = n
	}
	return out, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
