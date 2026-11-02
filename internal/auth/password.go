// Adapted from HallPass internal/opauth/password.go (Apache-2.0).

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
)

// Password length bounds after NFKC normalization. Longer input is rejected, never truncated.
const (
	MinPasswordBytes = 12
	MaxPasswordBytes = 1024
)

// Params are Argon2id parameters.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	KeyLen  uint32
	SaltLen int
}

// DefaultParams are used for new hashes (m=64 MiB, t=3, p=2). Hashes record their parameters; logins rehash hashes
// made with different parameters.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 2, KeyLen: 32, SaltLen: 16}

// TestParams are cheap parameters for tests only.
var TestParams = Params{Memory: 64, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

// NormalizePassword applies NFKC and enforces length bounds.
func NormalizePassword(pw string, minBytes int) ([]byte, error) {
	if len(pw) > 4*MaxPasswordBytes {
		return nil, apperr.New(apperr.CodeInvalid, "password must be at most %d bytes", MaxPasswordBytes)
	}
	n := norm.NFKC.String(pw)
	switch {
	case strings.ContainsRune(n, 0):
		return nil, apperr.New(apperr.CodeInvalid, "password must not contain NUL characters")
	case len(n) > MaxPasswordBytes:
		return nil, apperr.New(apperr.CodeInvalid, "password must be at most %d bytes", MaxPasswordBytes)
	case len(n) < minBytes:
		return nil, apperr.New(apperr.CodeInvalid, "password must be at least %d bytes", minBytes)
	}
	return []byte(n), nil
}

// HashPassword returns a PHC-format Argon2id hash: $argon2id$v=19$m=…,t=…,p=…$salt$hash.
func HashPassword(pw []byte, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey(pw, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("malformed password hash")

// VerifyPassword checks pw against a PHC hash in constant time. needsRehash is true when the hash was made with
// parameters other than p.
func VerifyPassword(pw []byte, phc string, p Params) (ok, needsRehash bool, err error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, false, errBadHash
	}
	var m, t uint32
	var th uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil || m == 0 || t == 0 || th == 0 ||
		m > 4*1024*1024 || t > 100 {
		return false, false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, false, errBadHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, false, errBadHash
	}
	got := argon2.IDKey(pw, salt, t, m, th, uint32(len(want))) //nolint:gosec // bounded above
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, ok && (m != p.Memory || t != p.Time || th != p.Threads || uint32(len(want)) != p.KeyLen), nil //nolint:gosec // bounded
}

// dummyHash is verified for unknown accounts so that response time does not reveal whether an account exists.
func dummyHash(p Params) string {
	dummyMu.Lock()
	defer dummyMu.Unlock()
	if h, ok := dummyByParams[p]; ok {
		return h
	}
	h, err := HashPassword([]byte("wagenpulse-dummy-password-for-timing"), p)
	if err != nil {
		panic(err)
	}
	dummyByParams[p] = h
	return h
}

var (
	dummyMu       sync.Mutex
	dummyByParams = map[Params]string{}
)
