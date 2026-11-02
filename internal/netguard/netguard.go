// Package netguard builds outbound HTTP clients that refuse to connect to internal addresses (SSRF guard for upstream
// OIDC discovery, token exchange, S3 and directory APIs). The check runs on the resolved IP at dial time, so DNS
// rebinding cannot bypass it; redirects are re-checked by the same dialer.
package netguard

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlocked means the destination address is not allowed.
var ErrBlocked = errors.New("outbound connection to an internal address refused")

// Guard decides which destination addresses are allowed.
type Guard struct {
	allow        []netip.Prefix
	deny         []netip.Prefix
	allowPrivate bool
	roots        *x509.CertPool // nil: system roots
}

// AddRootCAs trusts extra CA certificates (PEM) in addition to the system roots, e.g. a private CA of a LAN identity
// provider or MinIO (outbound.ca_file).
func (g *Guard) AddRootCAs(pem []byte) error {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("outbound.ca_file: no PEM certificates found")
	}
	g.roots = pool
	return nil
}

// Options configure a guard (ADR-0019).
type Options struct {
	// AllowCIDRs are explicitly permitted destinations (checked first; e.g. a loopback NATS in development).
	AllowCIDRs []string
	// DenyCIDRs are refused even when otherwise allowed.
	DenyCIDRs []string
	// AllowPrivate permits RFC 1918 and unique-local destinations (self-hosted NATS usually lives on a LAN).
	AllowPrivate bool
}

// New returns a guard that additionally allows the given CIDRs (e.g. a test IdP on 127.0.0.1/32).
func New(allowCIDRs []string) (*Guard, error) {
	return NewWithOptions(Options{AllowCIDRs: allowCIDRs})
}

// NewWithOptions returns a guard configured by o.
func NewWithOptions(o Options) (*Guard, error) {
	g := &Guard{allowPrivate: o.AllowPrivate}
	for _, s := range o.AllowCIDRs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("outbound allow %q: %w", s, err)
		}
		g.allow = append(g.allow, p.Masked())
	}
	for _, s := range o.DenyCIDRs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("outbound deny %q: %w", s, err)
		}
		g.deny = append(g.deny, p.Masked())
	}
	return g, nil
}

// Allowed reports whether a may be dialed. Cloud metadata, link-local, multicast and unspecified addresses are
// always refused; deny CIDRs win over everything except nothing; allow CIDRs win over the default refusals of
// loopback and private ranges.
func (g *Guard) Allowed(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range alwaysBlocked {
		if p.Contains(a) {
			return false
		}
	}
	if a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range g.deny {
		if p.Contains(a) {
			return false
		}
	}
	for _, p := range g.allow {
		if p.Contains(a) {
			return true
		}
	}
	if a.IsLoopback() {
		return false
	}
	if a.IsPrivate() {
		return g.allowPrivate
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// alwaysBlocked are cloud metadata endpoints; no option re-enables them.
var alwaysBlocked = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

// Dialer returns a net.Dialer that enforces the guard on every resolved address (NATS and other raw TCP clients).
func (g *Guard) Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: g.control}
}

// blocked are special-purpose ranges not covered by netip's predicates.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 can reach IPv4 internals
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local
	netip.MustParsePrefix("::/96"),           // IPv4-compatible (::a.b.c.d)
	netip.MustParsePrefix("::ffff:0:0:0/96"), // IPv4-translated
	netip.MustParsePrefix("2002::/16"),       // 6to4 embeds an IPv4 address
	netip.MustParsePrefix("2001::/32"),       // Teredo embeds an IPv4 address
}

func (g *Guard) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !g.Allowed(a) {
		return fmt.Errorf("%w: %s", ErrBlocked, a)
	}
	return nil
}

// Transport returns an HTTP transport whose dialer enforces the guard.
func (g *Guard) Transport() *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: g.control}
	var tc *tls.Config
	if g.roots != nil {
		tc = &tls.Config{RootCAs: g.roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Transport{
		TLSClientConfig:       tc,
		Proxy:                 nil, // never route through an environment proxy that could reach internals
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
}

// Client returns an HTTP client using the guard, with conservative timeouts.
func (g *Guard) Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: g.Transport(), CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}
