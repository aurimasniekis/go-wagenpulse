// Adapted from Porch internal/obs/metrics/metrics.go (Apache-2.0).

// Package metrics defines WagenPulse's Prometheus metrics. Every method is safe on a nil *Metrics (metrics disabled).
// Label values are bounded enums, kinds or internal IDs of configured objects (adapters, sources); VINs, user names
// and emails are never used as labels.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TxResult classifies a write transaction outcome.
type TxResult string

// Transaction results.
const (
	TxOK         TxResult = "ok"
	TxCanceled   TxResult = "canceled"
	TxBusy       TxResult = "busy"
	TxFull       TxResult = "full"
	TxIO         TxResult = "io"
	TxCorrupt    TxResult = "corrupt"
	TxConstraint TxResult = "constraint"
	TxConflict   TxResult = "conflict"
	TxNotFound   TxResult = "not_found"
	TxError      TxResult = "error"
)

// Metrics holds every WagenPulse collector in its own registry.
type Metrics struct {
	reg *prometheus.Registry

	storeTx       *prometheus.HistogramVec
	storeBusy     prometheus.Counter
	storeWait     prometheus.Histogram
	storeQueue    prometheus.Gauge
	storeDBBytes  prometheus.Gauge
	storeWALBytes prometheus.Gauge

	httpReqs *prometheus.CounterVec
	httpDur  *prometheus.HistogramVec

	logins      *prometheus.CounterVec
	auditEvents *prometheus.CounterVec
	jobRuns     *prometheus.CounterVec
	jobQueue    *prometheus.GaugeVec

	ingestDeliveries   *prometheus.CounterVec
	ingestObservations *prometheus.CounterVec
	ingestDuration     prometheus.Histogram
	collectRuns        *prometheus.CounterVec
	collectLag         *prometheus.GaugeVec
	ruleEvals          *prometheus.CounterVec
	eventsCreated      *prometheus.CounterVec
	deliveryAttempts   *prometheus.CounterVec
	deliveryLatency    *prometheus.HistogramVec
	deliveryQueue      *prometheus.GaugeVec
	adapterConnected   *prometheus.GaugeVec

	ready     prometheus.Gauge
	buildInfo *prometheus.GaugeVec
}

const ns = "wagenpulse"

// New registers every collector in a fresh registry (plus Go runtime and process collectors).
func New() *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry()}
	m.storeTx = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Subsystem: "store", Name: "tx_duration_seconds",
		Help: "Write transaction duration by name and result.", Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}},
		[]string{"tx", "result"})
	m.storeBusy = prometheus.NewCounter(prometheus.CounterOpts{Namespace: ns, Subsystem: "store", Name: "tx_busy_retries_total",
		Help: "Write transaction attempts retried after SQLITE_BUSY."})
	m.storeWait = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Subsystem: "store", Name: "writer_wait_seconds",
		Help: "Time waiting for the single writer connection and its write lock.", Buckets: prometheus.DefBuckets})
	m.storeQueue = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "store", Name: "writer_queue",
		Help: "Write transactions waiting for the writer."})
	m.storeDBBytes = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "store", Name: "db_bytes", Help: "Database file size."})
	m.storeWALBytes = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "store", Name: "wal_bytes", Help: "WAL file size."})
	m.httpReqs = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "http", Name: "requests_total",
		Help: "HTTP requests by surface, method and status code."}, []string{"surface", "method", "code"})
	m.httpDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Subsystem: "http", Name: "request_duration_seconds",
		Help: "HTTP request duration by surface.", Buckets: prometheus.DefBuckets}, []string{"surface"})
	m.logins = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "auth", Name: "logins_total",
		Help: "Login attempts by method and outcome."}, []string{"method", "outcome"})
	m.auditEvents = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "audit", Name: "events_total",
		Help: "Audit events appended by outcome."}, []string{"outcome"})
	m.jobRuns = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "jobs", Name: "runs_total",
		Help: "Background job runs by kind and result."}, []string{"kind", "result"})
	m.jobQueue = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Subsystem: "jobs", Name: "queue",
		Help: "Jobs by kind and state (queued, running, dead)."}, []string{"kind", "state"})
	m.ingestDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "ingest", Name: "deliveries_total",
		Help: "Raw deliveries processed by origin and outcome (parsed, duplicate, rejected, unrecognized)."}, []string{"origin", "outcome"})
	m.ingestObservations = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "ingest", Name: "observations_total",
		Help: "Observations by outcome (accepted, duplicate, rejected, correction)."}, []string{"outcome"})
	m.ingestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Subsystem: "ingest", Name: "delivery_duration_seconds",
		Help: "Time to parse, normalize and store one delivery.", Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}})
	m.collectRuns = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "collect", Name: "runs_total",
		Help: "Collection runs by source kind and outcome."}, []string{"kind", "outcome"})
	m.collectLag = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Subsystem: "collect", Name: "lag_seconds",
		Help: "Seconds since the last successful collection, by source ID."}, []string{"source"})
	m.ruleEvals = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "rules", Name: "evaluations_total",
		Help: "Rule evaluations by rule kind and result (fired, suppressed, no_match, late_skipped)."}, []string{"kind", "result"})
	m.eventsCreated = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "events", Name: "created_total",
		Help: "Events created by type and whether they are backfill."}, []string{"type", "backfill"})
	m.deliveryAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: "delivery", Name: "attempts_total",
		Help: "Delivery attempts by adapter ID and result."}, []string{"adapter", "result"})
	m.deliveryLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Subsystem: "delivery", Name: "latency_seconds",
		Help: "Time from event creation to confirmed delivery, by adapter ID.", Buckets: []float64{.01, .05, .1, .5, 1, 5, 15, 60, 300, 1800, 3600}},
		[]string{"adapter"})
	m.deliveryQueue = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Subsystem: "delivery", Name: "queue",
		Help: "Deliveries by adapter ID and state (pending, retrying, dead)."}, []string{"adapter", "state"})
	m.adapterConnected = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Subsystem: "adapter", Name: "connected",
		Help: "1 when the adapter's connection is up, by adapter ID."}, []string{"adapter"})
	m.ready = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "ready", Help: "1 when the instance reports ready."})
	m.buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Name: "build_info", Help: "Build information."},
		[]string{"version", "go_version"})
	m.reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.storeTx, m.storeBusy, m.storeWait, m.storeQueue, m.storeDBBytes, m.storeWALBytes, m.httpReqs, m.httpDur,
		m.logins, m.auditEvents, m.jobRuns, m.jobQueue, m.ingestDeliveries, m.ingestObservations, m.ingestDuration,
		m.collectRuns, m.collectLag, m.ruleEvals, m.eventsCreated, m.deliveryAttempts, m.deliveryLatency, m.deliveryQueue,
		m.adapterConnected, m.ready, m.buildInfo)
	return m
}

// Registry returns the underlying registry.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// StoreTx records a finished write transaction.
func (m *Metrics) StoreTx(name string, d time.Duration, r TxResult) {
	if m == nil {
		return
	}
	if name == "" {
		name = "unnamed"
	}
	m.storeTx.WithLabelValues(name, string(r)).Observe(d.Seconds())
}

// StoreTxBusyRetry counts a busy retry.
func (m *Metrics) StoreTxBusyRetry() {
	if m != nil {
		m.storeBusy.Inc()
	}
}

// StoreWriterWait records time spent waiting for the writer.
func (m *Metrics) StoreWriterWait(d time.Duration) {
	if m != nil {
		m.storeWait.Observe(d.Seconds())
	}
}

// StoreWriterQueue adjusts the writer queue gauge.
func (m *Metrics) StoreWriterQueue(delta int) {
	if m != nil {
		m.storeQueue.Add(float64(delta))
	}
}

// SetStoreSizes records database and WAL sizes.
func (m *Metrics) SetStoreSizes(dbBytes, walBytes int64) {
	if m != nil {
		m.storeDBBytes.Set(float64(dbBytes))
		m.storeWALBytes.Set(float64(walBytes))
	}
}

// HTTPRequest records one HTTP request.
func (m *Metrics) HTTPRequest(surface, method string, status int, d time.Duration) {
	if m == nil {
		return
	}
	m.httpReqs.WithLabelValues(surface, methodLabel(method), strconv.Itoa(status)).Inc()
	m.httpDur.WithLabelValues(surface).Observe(d.Seconds())
}

func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return "other"
}

// Login counts a login attempt (method password|totp|oidc; outcome success|failure|throttled|error).
func (m *Metrics) Login(method, outcome string) {
	if m != nil {
		m.logins.WithLabelValues(method, outcome).Inc()
	}
}

// AuditEvent counts an appended audit event.
func (m *Metrics) AuditEvent(outcome string) {
	if m != nil {
		m.auditEvents.WithLabelValues(outcome).Inc()
	}
}

// JobRun counts a job execution.
func (m *Metrics) JobRun(kind string, err error) {
	if m == nil {
		return
	}
	res := "ok"
	if err != nil {
		res = "error"
	}
	m.jobRuns.WithLabelValues(kind, res).Inc()
}

// SetJobQueue sets the queue gauge for (kind, state).
func (m *Metrics) SetJobQueue(kind, state string, n int64) {
	if m != nil {
		m.jobQueue.WithLabelValues(kind, state).Set(float64(n))
	}
}

// IngestDelivery counts a processed delivery.
func (m *Metrics) IngestDelivery(origin, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.ingestDeliveries.WithLabelValues(origin, outcome).Inc()
	if d > 0 {
		m.ingestDuration.Observe(d.Seconds())
	}
}

// IngestObservations counts observations by outcome.
func (m *Metrics) IngestObservations(outcome string, n int) {
	if m != nil && n > 0 {
		m.ingestObservations.WithLabelValues(outcome).Add(float64(n))
	}
}

// CollectRun counts a collection run.
func (m *Metrics) CollectRun(kind, outcome string) {
	if m != nil {
		m.collectRuns.WithLabelValues(kind, outcome).Inc()
	}
}

// SetCollectLag sets the lag gauge for a source.
func (m *Metrics) SetCollectLag(source string, lag time.Duration) {
	if m != nil {
		m.collectLag.WithLabelValues(source).Set(lag.Seconds())
	}
}

// RuleEval counts rule evaluations.
func (m *Metrics) RuleEval(kind, result string, n int) {
	if m != nil && n > 0 {
		m.ruleEvals.WithLabelValues(kind, result).Add(float64(n))
	}
}

// EventCreated counts a created event.
func (m *Metrics) EventCreated(typ string, backfill bool) {
	if m != nil {
		m.eventsCreated.WithLabelValues(typ, strconv.FormatBool(backfill)).Inc()
	}
}

// DeliveryAttempt counts a delivery attempt.
func (m *Metrics) DeliveryAttempt(adapter, result string) {
	if m != nil {
		m.deliveryAttempts.WithLabelValues(adapter, result).Inc()
	}
}

// DeliveryLatency observes event-to-delivery latency.
func (m *Metrics) DeliveryLatency(adapter string, d time.Duration) {
	if m != nil {
		m.deliveryLatency.WithLabelValues(adapter).Observe(d.Seconds())
	}
}

// SetDeliveryQueue sets the delivery queue gauge.
func (m *Metrics) SetDeliveryQueue(adapter, state string, n int64) {
	if m != nil {
		m.deliveryQueue.WithLabelValues(adapter, state).Set(float64(n))
	}
}

// SetAdapterConnected records adapter connectivity.
func (m *Metrics) SetAdapterConnected(adapter string, up bool) {
	if m == nil {
		return
	}
	v := 0.0
	if up {
		v = 1
	}
	m.adapterConnected.WithLabelValues(adapter).Set(v)
}

// SetReady records readiness.
func (m *Metrics) SetReady(ready bool) {
	if m == nil {
		return
	}
	if ready {
		m.ready.Set(1)
	} else {
		m.ready.Set(0)
	}
}

// SetBuildInfo records build information.
func (m *Metrics) SetBuildInfo(version, goVersion string) {
	if m != nil {
		m.buildInfo.WithLabelValues(version, goVersion).Set(1)
	}
}
