// Login throttling and the password + TOTP flow are adapted from HallPass internal/opauth/login.go (Apache-2.0).

package auth

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

const (
	challengePrefix   = "wgp_chl_"
	challengeTTL      = 5 * time.Minute
	challengeAttempts = 5
)

// ErrThrottled is returned while a login key is throttled; it does not reveal whether the account exists.
var ErrThrottled = apperr.New(apperr.CodeRateLimited, "too many attempts; try again later")

// ErrLocalLoginDisabled is returned when password login is turned off.
var ErrLocalLoginDisabled = apperr.New(apperr.CodeForbidden, "password login is disabled")

func ipKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "ip:" + ip
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return "ip:" + p.String()
	}
	return "ip:" + a.String()
}

func accountKey(username string) string {
	return "acct:" + strings.ToLower(strings.TrimSpace(username))
}

func (svc *Service) throttled(ctx context.Context, keys ...string) (bool, error) {
	now := svc.now().UnixMicro()
	for _, k := range keys {
		t, err := svc.s.R().GetThrottle(ctx, k)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if t.NextAllowedAt > now {
			return true, nil
		}
	}
	return false, nil
}

func (svc *Service) recordFailure(ctx context.Context, tx *store.Tx, keys ...string) error {
	for _, k := range keys {
		failures := int64(1)
		if t, err := tx.Q.GetThrottle(ctx, k); err == nil {
			if tx.Now.Sub(time.UnixMicro(t.UpdatedAt)) < svc.opts.Decay {
				failures = t.Failures + 1
			}
		} else if !isNoRows(err) {
			return err
		}
		next := tx.Now
		if free := int64(svc.opts.FreeFailures); failures > free {
			d := time.Second << min(failures-free-1, 20)
			next = tx.Now.Add(min(d, svc.opts.MaxDelay))
		}
		if err := tx.Q.UpsertThrottle(ctx, db.UpsertThrottleParams{Key: k, Failures: failures, NextAllowedAt: next.UnixMicro(), UpdatedAt: tx.NowMicros()}); err != nil {
			return err
		}
	}
	return nil
}

// LoginResult is the outcome of a correct password: a session, or a TOTP challenge to complete.
type LoginResult struct {
	Session   *NewSession
	Challenge string // set when TOTP is required
	User      User
}

// Login verifies a username and password. Unknown accounts, wrong passwords, suspended accounts and accounts without
// local login all fail with ErrInvalidCredentials after the same Argon2id work.
func (svc *Service) Login(ctx context.Context, username, password string, meta SessionMeta) (*LoginResult, error) {
	res, err := svc.login(ctx, username, password, meta)
	switch {
	case err == nil:
		svc.opts.Metrics.Login(MethodPassword, "success")
	case errors.Is(err, ErrThrottled):
		svc.opts.Metrics.Login(MethodPassword, "throttled")
	case errors.Is(err, ErrInvalidCredentials):
		svc.opts.Metrics.Login(MethodPassword, "failure")
	case errors.Is(err, ErrLocalLoginDisabled):
	default:
		svc.opts.Metrics.Login(MethodPassword, "error")
	}
	return res, err
}

func (svc *Service) login(ctx context.Context, username, password string, meta SessionMeta) (*LoginResult, error) {
	sys, err := svc.settings.System(ctx)
	if err != nil {
		return nil, err
	}
	if !sys.Value.LocalLoginEnabled {
		return nil, ErrLocalLoginDisabled
	}
	aKey, iKey := accountKey(username), ipKey(meta.IP)
	if t, err := svc.throttled(ctx, aKey, iKey); err != nil {
		return nil, err
	} else if t {
		return nil, ErrThrottled
	}
	pw, perr := NormalizePassword(password, 1)
	name, nerr := NormalizeUsername(username)
	var u db.User
	found := false
	if nerr == nil {
		if r, err := svc.s.R().GetUserByUsername(ctx, name); err == nil {
			u, found = r, true
		} else if !isNoRows(err) {
			return nil, err
		}
	}
	hash := dummyHash(svc.opts.Params)
	usable := found && u.Status == "active" && u.LocalLoginEnabled == 1 && u.PasswordHash.Valid && u.PasswordHash.String != ""
	if usable {
		hash = u.PasswordHash.String
	}
	ok, rehash := false, false
	if perr == nil {
		ok, rehash, _ = VerifyPassword(pw, hash, svc.opts.Params)
	}
	if !ok || !usable {
		ferr := svc.s.W().TxNamed(ctx, "auth.login_failed", func(tx *store.Tx) error {
			if err := svc.recordFailure(ctx, tx, aKey, iKey); err != nil {
				return err
			}
			actor := audit.Actor{Type: audit.ActorUser}
			if found {
				actor.ID = u.ID
			}
			_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "auth.login", Outcome: audit.OutcomeFailure,
				ReasonCode: "invalid_credentials", AuthMethod: MethodPassword, SourceIP: meta.IP})
			return err
		})
		if ferr != nil {
			return nil, ferr
		}
		return nil, ErrInvalidCredentials
	}
	var res LoginResult
	err = svc.s.W().TxNamed(ctx, "auth.login", func(tx *store.Tx) error {
		_ = tx.Q.DeleteThrottle(ctx, aKey)
		if rehash {
			if h, err := HashPassword(pw, svc.opts.Params); err == nil {
				if err := tx.Q.RehashUserPassword(ctx, db.RehashUserPasswordParams{PasswordHash: nullStr(h), ID: u.ID}); err != nil {
					return err
				}
			}
		}
		res.User = ToUser(u)
		if u.TotpEnabledAt.Valid {
			raw, err := randomToken(challengePrefix)
			if err != nil {
				return err
			}
			mac, err := svc.tokenMAC("challenge", raw)
			if err != nil {
				return err
			}
			if err := tx.Q.InsertChallenge(ctx, db.InsertChallengeParams{ID: ids.New(ids.Challenge), TokenMac: mac, UserID: u.ID,
				CreatedAt: tx.NowMicros(), ExpiresAt: tx.Now.Add(challengeTTL).UnixMicro()}); err != nil {
				return err
			}
			res.Challenge = raw
			return nil
		}
		s, err := svc.createSessionTx(ctx, tx, u, MethodPassword, "", meta)
		if err != nil {
			return err
		}
		res.Session = s
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: u.ID}, Action: "auth.login",
			AuthMethod: MethodPassword, SourceIP: meta.IP, Target: audit.Target{Type: "session", ID: s.ID}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// VerifyChallenge completes a password login with a TOTP code or a recovery code.
func (svc *Service) VerifyChallenge(ctx context.Context, challenge, code string, meta SessionMeta) (*LoginResult, error) {
	if !strings.HasPrefix(challenge, challengePrefix) {
		return nil, ErrInvalidCredentials
	}
	macs, err := svc.tokenMACs("challenge", challenge)
	if err != nil {
		return nil, err
	}
	var ch db.LoginChallenge
	found := false
	for _, m := range macs {
		if r, err := svc.s.R().GetChallengeByMAC(ctx, m); err == nil {
			ch, found = r, true
			break
		}
	}
	if !found || svc.now().UnixMicro() >= ch.ExpiresAt || ch.Attempts >= challengeAttempts {
		svc.opts.Metrics.Login("totp", "failure")
		return nil, apperr.New(apperr.CodeUnauthorized, "the sign-in attempt expired; sign in again")
	}
	if t, err := svc.throttled(ctx, ipKey(meta.IP)); err != nil {
		return nil, err
	} else if t {
		return nil, ErrThrottled
	}
	var res LoginResult
	var verr error
	err = svc.s.W().TxNamed(ctx, "auth.challenge", func(tx *store.Tx) error {
		verr = nil
		u, err := tx.Q.GetUser(ctx, ch.UserID)
		if err != nil {
			return err
		}
		ok, err := svc.checkSecondFactor(ctx, tx, u, code)
		if err != nil {
			return err
		}
		if !ok || u.Status != "active" {
			if err := tx.Q.BumpChallenge(ctx, ch.ID); err != nil {
				return err
			}
			if err := svc.recordFailure(ctx, tx, ipKey(meta.IP), accountKey(u.Username)); err != nil {
				return err
			}
			verr = apperr.New(apperr.CodeUnauthorized, "invalid verification code")
			_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: u.ID}, Action: "auth.login",
				Outcome: audit.OutcomeFailure, ReasonCode: "invalid_totp", AuthMethod: MethodPassword, SourceIP: meta.IP})
			return err
		}
		if err := tx.Q.DeleteChallenge(ctx, ch.ID); err != nil {
			return err
		}
		s, err := svc.createSessionTx(ctx, tx, u, MethodPassword, "", meta)
		if err != nil {
			return err
		}
		res.Session, res.User = s, ToUser(u)
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: u.ID}, Action: "auth.login",
			AuthMethod: MethodPassword, SourceIP: meta.IP, Target: audit.Target{Type: "session", ID: s.ID}, Details: map[string]any{"second_factor": true}})
		return err
	})
	if err != nil {
		return nil, err
	}
	if verr != nil {
		svc.opts.Metrics.Login("totp", "failure")
		return nil, verr
	}
	svc.opts.Metrics.Login("totp", "success")
	return &res, nil
}

// checkSecondFactor accepts a current TOTP code (no reuse) or an unused recovery code.
func (svc *Service) checkSecondFactor(ctx context.Context, tx *store.Tx, u db.User, code string) (bool, error) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(u.TotpSecretEnc) == 0 || !u.TotpEnabledAt.Valid {
		return false, nil
	}
	if len(code) == 6 {
		secret, err := svc.kr.Open(keyring.PurposeField, keyring.FieldAAD("users", "totp_secret_enc", u.ID), u.TotpSecretEnc)
		if err != nil {
			return false, err
		}
		step, ok := VerifyTOTP(secret, code, tx.Now, u.TotpLastStep)
		if !ok {
			return false, nil
		}
		n, err := tx.Q.SetUserTOTPStep(ctx, db.SetUserTOTPStepParams{TotpLastStep: step, ID: u.ID, TotpLastStep_2: step})
		return n == 1, err
	}
	norm := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	if len(norm) != recoveryCodeLen {
		return false, nil
	}
	macs, err := svc.tokenMACs("recovery", u.ID+"|"+norm)
	if err != nil {
		return false, err
	}
	for _, m := range macs {
		n, err := tx.Q.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UsedAt: nullInt(tx.NowMicros(), true), UserID: u.ID, CodeMac: m})
		if err != nil {
			return false, err
		}
		if n == 1 {
			return true, nil
		}
	}
	return false, nil
}

// LoginMethods describes what the login page offers.
type LoginMethods struct {
	Password  bool            `json:"password"`
	Providers []LoginProvider `json:"providers"`
}

// LoginProvider is an SSO button.
type LoginProvider struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// Methods returns the enabled login methods.
func (svc *Service) Methods(ctx context.Context) (LoginMethods, error) {
	sys, err := svc.settings.System(ctx)
	if err != nil {
		return LoginMethods{}, err
	}
	out := LoginMethods{Password: sys.Value.LocalLoginEnabled, Providers: []LoginProvider{}}
	if !sys.Value.OIDCLoginEnabled {
		return out, nil
	}
	rows, err := svc.s.R().ListOIDCProviders(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		if r.Enabled == 1 {
			out.Providers = append(out.Providers, LoginProvider{Slug: r.Slug, DisplayName: r.DisplayName})
		}
	}
	return out, nil
}

// guardSettings refuses system settings that would leave root without a usable login method (ADR-0012).
func (svc *Service) guardSettings(ctx context.Context, tx *store.Tx, next settings.System) error {
	root, err := tx.Q.GetRootUser(ctx)
	if isNoRows(err) {
		return nil // before bootstrap
	}
	if err != nil {
		return err
	}
	if rootCanLogin(ctx, tx, root, next) {
		return nil
	}
	return apperr.New(apperr.CodeConflict, "this change would leave the root user without a usable login method; "+
		"give root a local password or link an enabled SSO identity first")
}

// rootCanLogin reports whether root keeps at least one usable login method under s.
func rootCanLogin(ctx context.Context, tx *store.Tx, root db.User, s settings.System) bool {
	if s.LocalLoginEnabled && root.LocalLoginEnabled == 1 && root.PasswordHash.Valid && root.PasswordHash.String != "" {
		return true
	}
	if s.OIDCLoginEnabled {
		if n, err := tx.Q.CountEnabledIdentities(ctx, root.ID); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// CheckRootLoginTx re-checks root's login methods after a change inside tx (identity unlink, provider disable).
func CheckRootLoginTx(ctx context.Context, tx *store.Tx) error {
	root, err := tx.Q.GetRootUser(ctx)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sys, err := settings.SystemTx(ctx, tx)
	if err != nil {
		return err
	}
	if !rootCanLogin(ctx, tx, root, sys) {
		return apperr.New(apperr.CodeConflict, "this change would leave the root user without a usable login method")
	}
	return nil
}
