package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"

	"github.com/aurimasniekis/go-wagenpulse/internal/netguard"
)

// Message is one publication.
type Message struct {
	MsgID   string
	Subject string
	Type    string // ce-type
	Time    time.Time
	Body    []byte
	Headers map[string]string
}

// Ack is the destination's storage acknowledgement. For Pulse it is the JetStream PubAck: the event is stored in the
// EVENTS stream; it says nothing about consumer processing (ADR-0016).
type Ack struct {
	Stream    string
	Sequence  uint64
	Duplicate bool
}

// Publisher is an adapter instance's connection.
type Publisher interface {
	Publish(ctx context.Context, m Message) (*Ack, error)
	Connected() bool
	Close()
}

// PulsePublisher publishes through the Pulse Go SDK (adapted from pulse-github internal/publisher, MIT).
type PulsePublisher struct {
	c         *pulse.Client
	connected atomic.Bool
}

// PulseOptions configure a connection.
type PulseOptions struct {
	URL     string
	Creds   []byte
	CAPEM   []byte
	Source  string
	Name    string
	Guard   *netguard.Guard
	OnState func(connected bool, reason string)
	// OnAsyncError receives asynchronous NATS errors (for example permission violations) instead of stderr.
	OnAsyncError func(err error)
	ExtraNATS    []nats.Option
}

// ConnectPulse dials Pulse. The connection retries in the background when the server is unreachable, so a Pulse
// outage never blocks ingestion; deliveries wait in the outbox.
func ConnectPulse(o PulseOptions) (*PulsePublisher, error) {
	p := &PulsePublisher{}
	notify := func(c bool, reason string) {
		p.connected.Store(c)
		if o.OnState != nil {
			o.OnState(c, reason)
		}
	}
	nopts := []nats.Option{
		nats.RetryOnFailedConnect(true),
		nats.Timeout(10 * time.Second),
		nats.ConnectHandler(func(*nats.Conn) { notify(true, "") }),
		nats.ReconnectHandler(func(*nats.Conn) { notify(true, "") }),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			reason := "disconnected"
			if err != nil {
				reason = classifyConnErr(err)
			}
			notify(false, reason)
		}),
		nats.ClosedHandler(func(*nats.Conn) { p.connected.Store(false) }),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			if o.OnAsyncError != nil && err != nil {
				o.OnAsyncError(err)
			}
		}),
	}
	if o.Guard != nil {
		nopts = append(nopts, nats.SetCustomDialer(o.Guard.Dialer(10*time.Second)))
	}
	nopts = append(nopts, o.ExtraNATS...)
	copts := []pulse.Option{pulse.WithCreds(o.Creds), pulse.WithSource(o.Source), pulse.WithName(o.Name), pulse.WithNATSOptions(nopts...)}
	if len(o.CAPEM) > 0 {
		copts = append(copts, pulse.WithCA(o.CAPEM))
	}
	c, err := pulse.Connect(o.URL, copts...)
	if err != nil {
		return nil, err
	}
	p.c = c
	if c.Conn().IsConnected() {
		notify(true, "")
	}
	return p, nil
}

// Connected reports whether NATS is connected.
func (p *PulsePublisher) Connected() bool { return p.connected.Load() && p.c.Conn().IsConnected() }

// Publish publishes one event and waits for the JetStream acknowledgement.
func (p *PulsePublisher) Publish(ctx context.Context, m Message) (*Ack, error) {
	opts := []pulse.PublishOption{pulse.WithID(m.MsgID), pulse.WithTime(m.Time)}
	for k, v := range m.Headers {
		opts = append(opts, pulse.WithHeader(k, v))
	}
	ack, err := p.c.Publish(ctx, m.Subject, m.Type, json.RawMessage(m.Body), opts...)
	if err != nil {
		return nil, err
	}
	return &Ack{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// Close drains the connection (pending acknowledgements complete first).
func (p *PulsePublisher) Close() { p.c.Close() }

// Error classes (pulse-github Classify, adapted).
const (
	ClassInvalidSubject  = "invalid_subject"
	ClassPayloadTooLarge = "payload_too_large"
	ClassTimeout         = "publish_timeout"
	ClassNoStream        = "no_stream"
	ClassUnavailable     = "nats_unavailable"
	ClassRejected        = "publish_rejected"
	ClassBlocked         = "destination_blocked"
	ClassCreds           = "credentials_invalid"
	ClassPermission      = "permission_denied"
	ClassOther           = "publish_error"
)

const jsMessageTooLarge = 10054

// Classify maps a publish error to a stable class and whether retrying cannot succeed.
func Classify(err error) (class string, permanent bool) {
	var api *jetstream.APIError
	var legacyAPI *nats.APIError
	switch {
	case errors.Is(err, envelope.ErrSubject):
		return ClassInvalidSubject, true
	case errors.Is(err, nats.ErrMaxPayload):
		return ClassPayloadTooLarge, true
	case errors.As(err, &api) && api.ErrorCode == jsMessageTooLarge:
		return ClassPayloadTooLarge, true
	case errors.Is(err, jetstream.ErrNoStreamResponse), errors.Is(err, nats.ErrNoResponders):
		return ClassNoStream, false
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
		// Pulse reports a denied publish as a timeout: persistent timeouts while connected usually mean a missing grant.
		return ClassTimeout, false
	case errors.Is(err, nats.ErrConnectionClosed), errors.Is(err, nats.ErrDisconnected),
		errors.Is(err, nats.ErrConnectionReconnecting), errors.Is(err, nats.ErrConnectionDraining):
		return ClassUnavailable, false
	case errors.As(err, &api), errors.As(err, &legacyAPI):
		return ClassRejected, false
	default:
		return ClassOther, false
	}
}

func classifyConnErr(err error) string {
	switch {
	case errors.Is(err, nats.ErrPermissionViolation):
		return ClassPermission
	case errors.Is(err, netguard.ErrBlocked):
		return ClassBlocked
	case errors.Is(err, nats.ErrAuthorization), errors.Is(err, nats.ErrAuthExpired), errors.Is(err, nats.ErrAuthRevoked):
		return ClassCreds
	}
	return ClassUnavailable
}

// Hint explains an error class to operators.
func Hint(class string) string {
	switch class {
	case ClassTimeout:
		return "no acknowledgement in time; if the connection is up, the Pulse client probably lacks a publish grant for this subject"
	case ClassNoStream:
		return "no JetStream stream captures this subject; Pulse stores only evt.> subjects"
	case ClassInvalidSubject:
		return "the subject does not satisfy Pulse's grammar"
	case ClassPayloadTooLarge:
		return "the event is larger than the server's maximum payload"
	case ClassUnavailable:
		return "the NATS server is unreachable; deliveries wait and retry"
	case ClassBlocked:
		return "the destination address is refused by the outbound policy (outbound.allow_cidrs / deny_cidrs)"
	case ClassCreds:
		return "the NATS credentials were rejected, expired or revoked; issue new .creds in Pulse"
	case ClassRejected:
		return "the server refused the message"
	case ClassPermission:
		return "the Pulse client lacks a publish grant for this subject (pulse clients grant … publish:evt.<domain>.>)"
	}
	return ""
}
