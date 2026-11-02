// Package secretref resolves external secret references (ADR-0007): "env:WAGENPULSE_SECRET_NAME" reads an
// environment variable, "file:/abs/path" reads a file under an allowed directory. References are resolved at use time
// so rotating the external secret needs no API call. Resolved values are never logged or returned by the API.
package secretref

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
)

// MaxSecretBytes bounds a resolved secret (NATS creds files are a few KiB).
const MaxSecretBytes = 64 << 10

// Resolver resolves references under a policy.
type Resolver struct {
	EnvPrefix string
	FileDirs  []string
	Getenv    func(string) string
}

var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// ErrUnresolved means a reference names a secret that is not available.
var ErrUnresolved = errors.New("secret reference could not be resolved")

// Validate checks a reference's syntax and policy without reading it.
func (r *Resolver) Validate(ref string) error {
	kind, val, ok := strings.Cut(ref, ":")
	if !ok {
		return apperr.New(apperr.CodeInvalid, `secret reference must be "env:%sNAME" or "file:/absolute/path"`, r.EnvPrefix)
	}
	switch kind {
	case "env":
		if !envName.MatchString(val) || !strings.HasPrefix(val, r.EnvPrefix) || len(val) == len(r.EnvPrefix) {
			return apperr.New(apperr.CodeInvalid, "environment secret references must name a variable %sNAME", r.EnvPrefix)
		}
		return nil
	case "file":
		if !filepath.IsAbs(val) || filepath.Clean(val) != val {
			return apperr.New(apperr.CodeInvalid, "file secret references must be clean absolute paths")
		}
		for _, d := range r.FileDirs {
			if rel, err := filepath.Rel(d, val); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				return nil
			}
		}
		return apperr.New(apperr.CodeInvalid, "file secret references must be inside one of: %s", strings.Join(r.FileDirs, ", "))
	}
	return apperr.New(apperr.CodeInvalid, "unknown secret reference kind %q (use env: or file:)", kind)
}

// Resolve reads the secret behind ref.
func (r *Resolver) Resolve(ref string) ([]byte, error) {
	if err := r.Validate(ref); err != nil {
		return nil, err
	}
	kind, val, _ := strings.Cut(ref, ":")
	switch kind {
	case "env":
		get := r.Getenv
		if get == nil {
			get = os.Getenv
		}
		v := get(val)
		if v == "" {
			return nil, fmt.Errorf("%w: environment variable %s is empty or unset", ErrUnresolved, val)
		}
		if len(v) > MaxSecretBytes {
			return nil, fmt.Errorf("%w: %s is too large", ErrUnresolved, val)
		}
		return []byte(v), nil
	default:
		// Resolve symlinks so a link inside an allowed directory cannot point elsewhere.
		real, err := filepath.EvalSymlinks(val)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrUnresolved, val, errors.Unwrap(err))
		}
		resolvedDirs := make([]string, 0, len(r.FileDirs))
		for _, d := range r.FileDirs {
			if rd, err := filepath.EvalSymlinks(d); err == nil {
				resolvedDirs = append(resolvedDirs, rd)
			}
		}
		if err := (&Resolver{EnvPrefix: r.EnvPrefix, FileDirs: resolvedDirs}).Validate("file:" + real); err != nil {
			return nil, fmt.Errorf("%w: %s resolves outside the allowed directories", ErrUnresolved, val)
		}
		f, err := os.Open(real) //nolint:gosec // path validated against the allowed directories
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrUnresolved, val)
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, MaxSecretBytes+1))
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrUnresolved, val)
		}
		if len(b) > MaxSecretBytes {
			return nil, fmt.Errorf("%w: %s is too large", ErrUnresolved, val)
		}
		return b, nil
	}
}

// Fingerprint returns a short, non-reversible fingerprint of a secret for change detection: the first 12 hex
// characters of HMAC-SHA256(key, secret). key is a per-installation MAC key so fingerprints cannot be brute-forced
// offline from a leaked API response.
func Fingerprint(key, secret []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(secret)
	return hex.EncodeToString(m.Sum(nil))[:12]
}
