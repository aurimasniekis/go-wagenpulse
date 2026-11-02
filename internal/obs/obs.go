// Package obs provides the process logger (ECS-aligned JSON, redacted), correlation identifiers carried in contexts
// and small helpers shared by every component.
//
// Adapted from HallPass internal/obs/log.go and correlation.go (Apache-2.0), without OpenTelemetry.
package obs

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/buildinfo"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/redact"
)

// ECS-aligned field names used across WagenPulse logs (docs/operator/observability.md).
const (
	FieldTimestamp     = "@timestamp"
	FieldLevel         = "log.level"
	FieldMessage       = "message"
	FieldServiceName   = "service.name"
	FieldServiceVer    = "service.version"
	FieldError         = "error.message"
	FieldHTTPRequestID = "http.request.id"
	FieldUserID        = "user.id"
	FieldComponent     = "wagenpulse.component"
	FieldJobID         = "wagenpulse.job_id"
	FieldImportID      = "wagenpulse.import_id"
	FieldVehicleID     = "wagenpulse.vehicle_id"
	FieldSourceID      = "wagenpulse.source_id"
	FieldEventID       = "wagenpulse.event_id"
	FieldDeliveryID    = "wagenpulse.delivery_id"
	FieldAdapterID     = "wagenpulse.adapter_id"
	FieldCorrelationID = "wagenpulse.correlation_id"
	FieldTokenID       = "wagenpulse.token_id"
)

// NewLogger builds the process logger. JSON output uses ECS-aligned top-level keys; every handler redacts sensitive
// attribute keys; records logged with a context carry its correlation IDs. The LevelVar allows runtime changes.
func NewLogger(w io.Writer, level, format string) (*slog.Logger, *slog.LevelVar) {
	lv := new(slog.LevelVar)
	lv.Set(ParseLevel(level))
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if format == "text" {
		opts.ReplaceAttr = redact.ReplaceAttr
		h = slog.NewTextHandler(w, opts)
	} else {
		opts.ReplaceAttr = ecsReplace
		h = slog.NewJSONHandler(w, opts)
	}
	l := slog.New(&contextHandler{next: h}).With(
		slog.String(FieldServiceName, "wagenpulse"),
		slog.String(FieldServiceVer, buildinfo.Get().Version),
	)
	return l, lv
}

// ParseLevel converts a config level name; unknown values map to info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func ecsReplace(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		switch a.Key {
		case slog.TimeKey:
			if t, ok := a.Value.Any().(time.Time); ok {
				return slog.String(FieldTimestamp, t.UTC().Format(time.RFC3339Nano))
			}
			return slog.Attr{Key: FieldTimestamp, Value: a.Value}
		case slog.LevelKey:
			return slog.String(FieldLevel, strings.ToLower(a.Value.String()))
		case slog.MessageKey:
			return slog.Attr{Key: FieldMessage, Value: a.Value}
		}
	}
	return redact.ReplaceAttr(groups, a)
}

// Err returns the standard error attribute.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	return slog.String(FieldError, err.Error())
}

// Component returns a logger tagged with a component name.
func Component(l *slog.Logger, name string) *slog.Logger {
	return l.With(slog.String(FieldComponent, name))
}

// Discard is a logger that drops everything (tests, optional dependencies).
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// MaskVIN keeps the last six characters of a VIN for logs and non-privileged views.
func MaskVIN(vin string) string {
	if len(vin) <= 6 {
		return strings.Repeat("•", len(vin))
	}
	return strings.Repeat("•", len(vin)-6) + vin[len(vin)-6:]
}

type corrID struct{ key, value string }

type corrKey struct{}

// WithID returns a context carrying a correlation identifier under one of the Field* keys. Values are identifiers,
// never secrets; they are truncated to 128 bytes.
func WithID(ctx context.Context, key, value string) context.Context {
	if value == "" {
		return ctx
	}
	if len(value) > 128 {
		value = value[:128]
	}
	cur, _ := ctx.Value(corrKey{}).([]corrID)
	next := slices.Clone(cur)
	if i := slices.IndexFunc(next, func(c corrID) bool { return c.key == key }); i >= 0 {
		next[i].value = value
	} else {
		next = append(next, corrID{key, value})
	}
	return context.WithValue(ctx, corrKey{}, next)
}

// ID returns the correlation identifier stored under key, or "".
func ID(ctx context.Context, key string) string {
	if ctx == nil {
		return ""
	}
	cur, _ := ctx.Value(corrKey{}).([]corrID)
	for _, c := range cur {
		if c.key == key {
			return c.value
		}
	}
	return ""
}

// contextHandler adds the correlation IDs of the record's context as top-level attributes.
type contextHandler struct {
	next slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if cur, _ := ctx.Value(corrKey{}).([]corrID); len(cur) > 0 {
			for _, c := range cur {
				r.AddAttrs(slog.String(c.key, c.value))
			}
		}
	}
	return h.next.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{next: h.next.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{next: h.next.WithGroup(name)}
}
