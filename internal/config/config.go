// Adapted from Porch internal/config/config.go (Apache-2.0).

// Package config loads and validates WagenPulse configuration.
//
// Configuration is deployment/bootstrap-only: where data lives, how the process listens, where the key-encryption
// key comes from and process-wide limits. Everything an operator changes at runtime (vehicles, sources, rules,
// adapters, retention, login methods, OIDC providers) lives in the database and is managed through the API, the UI or
// Terraform (docs/DECISIONS.md ADR-0004).
//
// Configuration comes from a YAML file plus WAGENPULSE_* environment overrides. Unknown keys are rejected. The runtime
// mode is "production" unless explicitly set to "development"; development-only conveniences are rejected in
// production.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Mode is the runtime mode.
type Mode string

// Runtime modes.
const (
	ModeProduction  Mode = "production"
	ModeDevelopment Mode = "development"
)

// Duration is a time.Duration that unmarshals from Go duration strings ("30s", "15m").
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration must be a string like \"30s\": %w", n.Line, err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Config is the complete WagenPulse configuration.
type Config struct {
	Mode       Mode             `yaml:"mode"`
	DataDir    string           `yaml:"data_dir"`
	PublicURL  string           `yaml:"public_url"`
	KEK        KEKConfig        `yaml:"kek"`
	HTTP       HTTPConfig       `yaml:"http"`
	Management ManagementConfig `yaml:"management"`
	Proxy      ProxyConfig      `yaml:"proxy"`
	Log        LogConfig        `yaml:"log"`
	Storage    StorageConfig    `yaml:"storage"`
	Session    SessionConfig    `yaml:"session"`
	Setup      SetupConfig      `yaml:"setup"`
	Login      LoginConfig      `yaml:"login"`
	Outbound   OutboundConfig   `yaml:"outbound"`
	Ingest     IngestConfig     `yaml:"ingest"`
	Workers    WorkersConfig    `yaml:"workers"`
	Secrets    SecretsConfig    `yaml:"secrets"`
	Demo       DemoConfig       `yaml:"demo"`
}

// SecretsConfig limits external secret references (ADR-0007). Environment references must be named
// WAGENPULSE_SECRET_*; file references must live under one of FileDirs. This keeps an administrator from pointing a
// reference at the KEK or another process secret and exfiltrating it through an attacker-controlled endpoint.
type SecretsConfig struct {
	FileDirs []string `yaml:"file_dirs"`
}

// KEKConfig locates the master key-encryption key (ADR-0007). The KEK never lives inside data_dir in production.
type KEKConfig struct {
	// Source is "file" (default), "credential" (systemd LoadCredential=: File names the credential) or "env"
	// (reads base64 from WAGENPULSE_KEK; discouraged).
	Source string `yaml:"source"`
	File   string `yaml:"file"`
}

// HTTPConfig configures the public listener (UI + API).
type HTTPConfig struct {
	Listen            string   `yaml:"listen"`
	TLSCertFile       string   `yaml:"tls_cert_file"`
	TLSKeyFile        string   `yaml:"tls_key_file"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	ReadTimeout       Duration `yaml:"read_timeout"`
	WriteTimeout      Duration `yaml:"write_timeout"`
	IdleTimeout       Duration `yaml:"idle_timeout"`
	MaxHeaderBytes    int      `yaml:"max_header_bytes"`
	// MaxBodyBytes bounds JSON request bodies. Uploads have their own limit (ingest.max_upload_bytes).
	MaxBodyBytes int64 `yaml:"max_body_bytes"`
	// RequestTimeout bounds the handler time of ordinary API requests.
	RequestTimeout Duration `yaml:"request_timeout"`
}

// ManagementConfig configures the private management listener (metrics, health, pprof).
type ManagementConfig struct {
	Listen string `yaml:"listen"`
	Pprof  bool   `yaml:"pprof"`
}

// ProxyConfig decides which peers may set client-IP headers.
type ProxyConfig struct {
	// TrustedProxies are CIDRs of reverse proxies whose X-Forwarded-For entries are believed.
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// StorageConfig configures storage safety.
type StorageConfig struct {
	// AllowNetworkFS overrides the network-filesystem refusal. Never set in production.
	AllowNetworkFS        bool `yaml:"allow_network_fs"`
	PreMigrationSnapshots int  `yaml:"pre_migration_snapshots"`
}

// SessionConfig configures browser sessions. Runtime-adjustable lifetimes live in system settings; these are the
// hard upper bounds the API refuses to exceed.
type SessionConfig struct {
	MaxIdle     Duration `yaml:"max_idle"`
	MaxAbsolute Duration `yaml:"max_absolute"`
}

// SetupConfig configures first-run setup tokens.
type SetupConfig struct {
	TokenTTL Duration `yaml:"token_ttl"`
}

// LoginConfig configures local password login throttling (hallpass pattern, ADR-0011).
type LoginConfig struct {
	// FreeFailures is how many failures per account or source are tolerated before delays start.
	FreeFailures int `yaml:"free_failures"`
	// MaxDelay caps the exponential delay.
	MaxDelay Duration `yaml:"max_delay"`
	// Decay forgets old failures.
	Decay Duration `yaml:"decay"`
}

// OutboundConfig controls outbound connections (SSRF guard, ADR-0019).
type OutboundConfig struct {
	// AllowCIDRs are extra destinations WagenPulse may connect to that are otherwise refused (loopback).
	AllowCIDRs []string `yaml:"allow_cidrs"`
	// DenyCIDRs are refused even when they would otherwise be allowed (for example, private ranges you never use).
	DenyCIDRs []string `yaml:"deny_cidrs"`
	// AllowPrivate permits RFC 1918 / ULA destinations (default true: self-hosted NATS usually lives on a LAN).
	AllowPrivate bool `yaml:"allow_private"`
	// CAFile adds PEM CA certificates trusted for outbound TLS (OIDC providers), on top of the system roots.
	CAFile string `yaml:"ca_file"`
}

// IngestConfig bounds uploads and archive extraction (ADR-0013).
type IngestConfig struct {
	MaxUploadBytes    int64 `yaml:"max_upload_bytes"`
	MaxExtractedBytes int64 `yaml:"max_extracted_bytes"`
	MaxEntries        int   `yaml:"max_entries"`
	MaxRatio          int   `yaml:"max_ratio"`
	// BatchSize is the number of observations written per transaction.
	BatchSize int `yaml:"batch_size"`
}

// WorkersConfig bounds background concurrency.
type WorkersConfig struct {
	// Jobs is the number of concurrent background jobs (imports, collection, sweeps).
	Jobs int `yaml:"jobs"`
	// DeliveryPerAdapter is the number of in-flight publishes per adapter instance.
	DeliveryPerAdapter int `yaml:"delivery_per_adapter"`
	// ShutdownGrace bounds draining on SIGTERM.
	ShutdownGrace Duration `yaml:"shutdown_grace"`
}

// DemoConfig allows loading clearly labelled synthetic demo data. It is refused in production mode.
type DemoConfig struct {
	Enabled bool `yaml:"enabled"`
}

// Load reads path (optional) and applies environment overrides from environ, then validates.
func Load(path string, environ []string) (*Config, error) {
	cfg := Default()
	if path != "" {
		f, err := os.Open(path) //nolint:gosec // operator-supplied config path
		if err != nil {
			return nil, fmt.Errorf("open config: %w", err)
		}
		defer f.Close()
		if err := decode(f, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if err := applyEnv(cfg, environ); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func decode(r io.Reader, cfg *Config) error {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("config must contain a single YAML document")
	}
	return nil
}

// IsProduction reports whether the configuration runs in production mode.
func (c *Config) IsProduction() bool { return c.Mode != ModeDevelopment }
