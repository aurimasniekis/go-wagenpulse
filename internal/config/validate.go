// Adapted from Porch internal/config/validate.go (Apache-2.0).

package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// ValidationError aggregates every configuration problem found.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid configuration:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Validate checks the configuration, including production-mode restrictions.
func (c *Config) Validate() error {
	v := &validator{}
	switch c.Mode {
	case ModeProduction, ModeDevelopment:
	default:
		v.add("mode must be %q or %q, got %q", ModeProduction, ModeDevelopment, c.Mode)
	}
	prod := c.IsProduction()
	if c.DataDir == "" || !filepath.IsAbs(c.DataDir) {
		v.add("data_dir must be an absolute path")
	}
	c.validatePublicURL(v, prod)
	c.validateKEK(v, prod)
	v.listen("http.listen", c.HTTP.Listen)
	if (c.HTTP.TLSCertFile == "") != (c.HTTP.TLSKeyFile == "") {
		v.add("http.tls_cert_file and http.tls_key_file must be set together")
	}
	v.durationRange("http.read_header_timeout", c.HTTP.ReadHeaderTimeout, time.Second, time.Minute)
	v.durationRange("http.read_timeout", c.HTTP.ReadTimeout, time.Second, 30*time.Minute)
	v.durationRange("http.write_timeout", c.HTTP.WriteTimeout, time.Second, 30*time.Minute)
	v.durationRange("http.idle_timeout", c.HTTP.IdleTimeout, time.Second, 30*time.Minute)
	v.durationRange("http.request_timeout", c.HTTP.RequestTimeout, time.Second, 10*time.Minute)
	v.positive("http.max_header_bytes", c.HTTP.MaxHeaderBytes)
	if c.HTTP.MaxBodyBytes <= 0 || c.HTTP.MaxBodyBytes > 64<<20 {
		v.add("http.max_body_bytes must be within 1..%d", 64<<20)
	}
	v.listen("management.listen", c.Management.Listen)
	if host, _, err := net.SplitHostPort(c.Management.Listen); err == nil && !IsLoopbackHost(host) {
		if prod && c.Management.Pprof {
			v.add("management.pprof must not be exposed on a non-loopback management.listen in production")
		}
	}
	for _, p := range c.Proxy.TrustedProxies {
		v.prefix("proxy.trusted_proxies", p)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		v.add("log.level must be debug, info, warn or error")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		v.add("log.format must be json or text")
	}
	if prod && c.Storage.AllowNetworkFS {
		v.add("storage.allow_network_fs is development-only")
	}
	v.positive("storage.pre_migration_snapshots", c.Storage.PreMigrationSnapshots)
	v.durationRange("session.max_idle", c.Session.MaxIdle, time.Minute, 90*24*time.Hour)
	v.durationRange("session.max_absolute", c.Session.MaxAbsolute, c.Session.MaxIdle.D(), 365*24*time.Hour)
	v.durationRange("setup.token_ttl", c.Setup.TokenTTL, time.Minute, 24*time.Hour)
	if c.Login.FreeFailures < 1 || c.Login.FreeFailures > 20 {
		v.add("login.free_failures must be within 1..20")
	}
	v.durationRange("login.max_delay", c.Login.MaxDelay, time.Second, 24*time.Hour)
	v.durationRange("login.decay", c.Login.Decay, time.Minute, 30*24*time.Hour)
	for _, p := range c.Outbound.AllowCIDRs {
		v.prefix("outbound.allow_cidrs", p)
	}
	for _, p := range c.Outbound.DenyCIDRs {
		v.prefix("outbound.deny_cidrs", p)
	}
	for _, d := range c.Secrets.FileDirs {
		if !filepath.IsAbs(d) {
			v.add("secrets.file_dirs entry %q must be an absolute path", d)
		}
		if isWithin(d, c.DataDir) {
			v.add("secrets.file_dirs entry %q must not be inside data_dir", d)
		}
	}
	in := c.Ingest
	if in.MaxUploadBytes < 1<<10 || in.MaxUploadBytes > 2<<30 {
		v.add("ingest.max_upload_bytes must be within 1KiB..2GiB")
	}
	if in.MaxExtractedBytes < in.MaxUploadBytes || in.MaxExtractedBytes > 8<<30 {
		v.add("ingest.max_extracted_bytes must be >= max_upload_bytes and <= 8GiB")
	}
	if in.MaxEntries < 1 || in.MaxEntries > 100000 {
		v.add("ingest.max_entries must be within 1..100000")
	}
	if in.MaxRatio < 2 || in.MaxRatio > 1000 {
		v.add("ingest.max_ratio must be within 2..1000")
	}
	if in.BatchSize < 10 || in.BatchSize > 20000 {
		v.add("ingest.batch_size must be within 10..20000")
	}
	if c.Workers.Jobs < 1 || c.Workers.Jobs > 64 {
		v.add("workers.jobs must be within 1..64")
	}
	if c.Workers.DeliveryPerAdapter < 1 || c.Workers.DeliveryPerAdapter > 16 {
		v.add("workers.delivery_per_adapter must be within 1..16")
	}
	v.durationRange("workers.shutdown_grace", c.Workers.ShutdownGrace, time.Second, 10*time.Minute)
	return v.err()
}

func (c *Config) validatePublicURL(v *validator, prod bool) {
	if c.PublicURL == "" {
		v.add("public_url is required (the externally reachable URL; it is the OIDC redirect base and the CSRF origin)")
		return
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		v.add("public_url must be an absolute URL without credentials, query or fragment")
		return
	}
	if u.Path != "" && u.Path != "/" {
		v.add("public_url must not contain a path (serve WagenPulse at the origin root)")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if prod && !IsLoopbackHost(u.Hostname()) {
			v.add("public_url must use https in production (http is allowed only for localhost)")
		}
	default:
		v.add("public_url scheme must be https")
	}
}

func (c *Config) validateKEK(v *validator, prod bool) {
	switch c.KEK.Source {
	case "file":
		if c.KEK.File == "" {
			if prod {
				v.add("kek.file is required in production (generate one with `wagenpulse keys init --out FILE`)")
			}
			return
		}
		if !filepath.IsAbs(c.KEK.File) {
			v.add("kek.file must be an absolute path")
			return
		}
		if prod && isWithin(c.KEK.File, c.DataDir) {
			v.add("kek.file must be stored outside data_dir in production")
		}
	case "env":
	case "credential":
		if c.KEK.File == "" || strings.ContainsAny(c.KEK.File, "/\\") {
			v.add("kek.file must name the systemd credential (e.g. wagenpulse-kek) when kek.source is credential")
		}
	default:
		v.add("kek.source must be file, env or credential")
	}
}

// PublicOrigin returns scheme://host of public_url without a trailing slash.
func (c *Config) PublicOrigin() string { return strings.TrimSuffix(c.PublicURL, "/") }

// SecureCookies reports whether cookies get the Secure attribute and the __Host- prefix.
func (c *Config) SecureCookies() bool { return strings.HasPrefix(c.PublicURL, "https://") }

// IsLoopbackHost reports whether host is localhost or a loopback IP.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWithin(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type validator struct{ problems []string }

func (v *validator) add(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) positive(name string, n int) {
	if n <= 0 {
		v.add("%s must be positive", name)
	}
}

func (v *validator) durationRange(name string, d Duration, lo, hi time.Duration) {
	if d.D() < lo || d.D() > hi {
		v.add("%s must be within %s..%s, got %s", name, lo, hi, d.D())
	}
}

func (v *validator) listen(name, addr string) {
	if _, port, err := net.SplitHostPort(addr); err != nil || port == "" {
		v.add("%s must be host:port, got %q", name, addr)
	}
}

func (v *validator) prefix(name, s string) {
	if _, err := netip.ParsePrefix(s); err != nil {
		v.add("%s entry %q is not a CIDR prefix", name, s)
	}
}

func (v *validator) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: v.problems}
}
