package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/clock"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring/keyringtest"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/storetest"
)

type env struct {
	svc   *Service
	s     *store.Store
	clock *clock.Fake
	root  *Principal
}

const rootPassword = "correct horse battery staple"

func newEnv(t *testing.T) *env {
	t.Helper()
	fc := clock.NewFake(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	s := storetest.Open(t, storetest.WithClock(fc))
	kr := keyringtest.Load(t, s)
	aw := audit.NewWriter(kr)
	st := settings.New(s, aw, 30*24*time.Hour, 365*24*time.Hour)
	svc := New(s, kr, aw, st, Options{Clock: fc, Params: TestParams})
	e := &env{svc: svc, s: s, clock: fc}
	ctx := context.Background()
	tok, _, err := svc.IssueSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.CompleteSetup(ctx, SetupInput{Token: tok, Username: "Root", Password: rootPassword}, SessionMeta{IP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if e.root, err = svc.Authenticate(ctx, res.Session.Token); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) userPrincipal(t *testing.T, role Role, name string) (*Principal, User) {
	t.Helper()
	ctx := context.Background()
	u, err := e.svc.CreateUser(ctx, e.root, UserInput{Username: name, Role: &role, Password: "a-long-enough-password"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Login(ctx, name, "a-long-enough-password", SessionMeta{IP: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.Authenticate(ctx, res.Session.Token)
	if err != nil {
		t.Fatal(err)
	}
	return p, u
}

func codeOf(err error) apperr.Code { return apperr.CodeOf(err) }

func TestSetupOnceAndRootIsSingle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if e.root.Role != RoleRoot || !e.root.IsRoot() {
		t.Fatalf("root principal %+v", e.root)
	}
	if _, _, err := e.svc.IssueSetupToken(ctx); codeOf(err) != apperr.CodeConflict {
		t.Fatalf("second setup token: %v", err)
	}
	st, err := e.svc.SetupStatus(ctx)
	if err != nil || !st.Completed {
		t.Fatalf("status %+v %v", st, err)
	}
	root := RoleRoot
	if _, err := e.svc.CreateUser(ctx, e.root, UserInput{Username: "second", Role: &root}); err == nil {
		t.Fatal("created a second root")
	}
}

func TestSetupTokenSingleUseAndExpiry(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	s := storetest.Open(t, storetest.WithClock(fc))
	kr := keyringtest.Load(t, s)
	aw := audit.NewWriter(kr)
	svc := New(s, kr, aw, settings.New(s, aw, 30*24*time.Hour, 365*24*time.Hour), Options{Clock: fc, Params: TestParams, SetupTTL: time.Minute})
	ctx := context.Background()
	tok, _, err := svc.IssueSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fc.Advance(2 * time.Minute)
	if _, err := svc.CompleteSetup(ctx, SetupInput{Token: tok, Username: "root", Password: rootPassword}, SessionMeta{IP: "192.0.2.1"}); codeOf(err) != apperr.CodeUnauthorized {
		t.Fatalf("expired token accepted: %v", err)
	}
	if _, err := svc.CompleteSetup(ctx, SetupInput{Token: "wgp_setup_nope", Username: "root", Password: rootPassword}, SessionMeta{IP: "192.0.2.1"}); codeOf(err) != apperr.CodeUnauthorized {
		t.Fatalf("bogus token accepted: %v", err)
	}
	if _, err := svc.CompleteSetup(ctx, SetupInput{Token: tok, Username: "root", Password: "short"}, SessionMeta{}); codeOf(err) != apperr.CodeInvalid {
		t.Fatalf("short password: %v", err)
	}
}

func TestLoginUniformFailureAndThrottle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := range 3 {
		_, err := e.svc.Login(ctx, "root", "wrong password!", SessionMeta{IP: "198.51.100.7"})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := e.svc.Login(ctx, "nobody", "wrong password!", SessionMeta{IP: "203.0.113.9"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	// Fourth failure starts a delay for the account key.
	_, _ = e.svc.Login(ctx, "root", "wrong password!", SessionMeta{IP: "198.51.100.7"})
	if _, err := e.svc.Login(ctx, "root", rootPassword, SessionMeta{IP: "198.51.100.8"}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("expected throttle, got %v", err)
	}
	e.clock.Advance(2 * time.Second)
	if _, err := e.svc.Login(ctx, "root", rootPassword, SessionMeta{IP: "198.51.100.8"}); err != nil {
		t.Fatalf("after delay: %v", err)
	}
}

func TestSessionExpiryLogoutAndCSRF(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, err := e.svc.Login(ctx, "root", rootPassword, SessionMeta{IP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.Authenticate(ctx, res.Session.Token)
	if err != nil {
		t.Fatal(err)
	}
	csrf := e.svc.CSRFToken(p)
	if !e.svc.VerifyCSRF(p, csrf) || e.svc.VerifyCSRF(p, csrf+"x") || e.svc.VerifyCSRF(e.root, csrf) {
		t.Fatal("csrf binding broken")
	}
	if err := e.svc.Logout(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, res.Session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session accepted: %v", err)
	}
	// Idle expiry (default 12 h).
	res2, _ := e.svc.Login(ctx, "root", rootPassword, SessionMeta{IP: "192.0.2.1"})
	e.clock.Advance(13 * time.Hour)
	if _, err := e.svc.Authenticate(ctx, res2.Session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle-expired session accepted: %v", err)
	}
}

func TestTOTPChallenge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	enr, err := e.svc.BeginTOTP(ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := b32.DecodeString(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := e.svc.ConfirmTOTP(ctx, e.root, TOTPCode(secret, TOTPStep(e.clock.Now())))
	if err != nil || len(codes) != recoveryCodeCount {
		t.Fatalf("confirm %v %v", codes, err)
	}
	res, err := e.svc.Login(ctx, "root", rootPassword, SessionMeta{IP: "192.0.2.1"})
	if err != nil || res.Challenge == "" || res.Session != nil {
		t.Fatalf("expected challenge: %+v %v", res, err)
	}
	if _, err := e.svc.VerifyChallenge(ctx, res.Challenge, "000000", SessionMeta{IP: "192.0.2.1"}); codeOf(err) != apperr.CodeUnauthorized {
		t.Fatalf("wrong code accepted: %v", err)
	}
	// The confirmation code's step was consumed; use the next step.
	e.clock.Advance(30 * time.Second)
	ok, err := e.svc.VerifyChallenge(ctx, res.Challenge, TOTPCode(secret, TOTPStep(e.clock.Now())), SessionMeta{IP: "192.0.2.1"})
	if err != nil || ok.Session == nil {
		t.Fatalf("valid code: %v", err)
	}
	// Recovery code works once.
	res, _ = e.svc.Login(ctx, "root", rootPassword, SessionMeta{IP: "192.0.2.1"})
	if _, err := e.svc.VerifyChallenge(ctx, res.Challenge, codes[0], SessionMeta{IP: "192.0.2.1"}); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	res, _ = e.svc.Login(ctx, "root", rootPassword, SessionMeta{IP: "192.0.2.1"})
	if _, err := e.svc.VerifyChallenge(ctx, res.Challenge, codes[0], SessionMeta{IP: "192.0.2.1"}); err == nil {
		t.Fatal("recovery code reused")
	}
}

func TestRoleHierarchy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin, _ := e.userPrincipal(t, RoleAdmin, "alice")
	_, op := e.userPrincipal(t, RoleOperator, "bob")
	adminRole := RoleAdmin
	if _, err := e.svc.CreateUser(ctx, admin, UserInput{Username: "carol", Role: &adminRole}); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("admin created an admin: %v", err)
	}
	if _, err := e.svc.UpdateUser(ctx, admin, op.ID, 0, UserInput{Role: &adminRole}); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("admin promoted to admin: %v", err)
	}
	viewer := RoleViewer
	if _, err := e.svc.UpdateUser(ctx, admin, op.ID, 0, UserInput{Role: &viewer}); err != nil {
		t.Fatalf("admin demoting operator: %v", err)
	}
	if _, err := e.svc.UpdateUser(ctx, admin, e.root.UserID, 0, UserInput{Role: &viewer}); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("admin modified root: %v", err)
	}
	if _, err := e.svc.UpdateUser(ctx, e.root, e.root.UserID, 0, UserInput{Role: &viewer}); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("root demoted itself: %v", err)
	}
	if err := e.svc.DeleteUser(ctx, admin, e.root.UserID); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("root deleted: %v", err)
	}
}

func TestRootLockoutGuard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sys := settings.DefaultSystem()
	sys.LocalLoginEnabled = false
	if _, err := e.svc.Settings().UpdateSystem(ctx, e.root.AuditActor(), 0, sys); codeOf(err) != apperr.CodeConflict {
		t.Fatalf("disabling local login without SSO identity for root: %v", err)
	}
	off := false
	if _, err := e.svc.UpdateUser(ctx, e.root, e.root.UserID, 0, UserInput{LocalLoginEnabled: &off}); codeOf(err) != apperr.CodeConflict {
		t.Fatalf("root disabling its only login method: %v", err)
	}
}

func TestTokenRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Non-expiring tokens: root only, explicit acknowledgement, not the default.
	if _, err := e.svc.CreateToken(ctx, e.root, TokenInput{Name: "tf", Scopes: []Permission{PermVehiclesRead}, NeverExpires: true}, nil); codeOf(err) != apperr.CodeInvalid {
		t.Fatalf("missing acknowledgement: %v", err)
	}
	main, err := e.svc.CreateToken(ctx, e.root, TokenInput{Name: "terraform", Scopes: PermissionPresets()["terraform"],
		NeverExpires: true, AcknowledgeNeverExpires: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(main.Secret, TokenPrefix) || main.ExpiresAt != nil || !main.NeverExpires {
		t.Fatalf("token %+v", main.Token)
	}
	def, err := e.svc.CreateToken(ctx, e.root, TokenInput{Name: "default", Scopes: []Permission{PermVehiclesRead}}, nil)
	if err != nil || def.ExpiresAt == nil || def.NeverExpires {
		t.Fatalf("default must expire: %+v %v", def, err)
	}
	admin, _ := e.userPrincipal(t, RoleAdmin, "alice")
	if _, err := e.svc.CreateToken(ctx, admin, TokenInput{Name: "x", Scopes: []Permission{PermVehiclesRead}, NeverExpires: true,
		AcknowledgeNeverExpires: true}, nil); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("admin minted non-expiring token: %v", err)
	}
	viewer, _ := e.userPrincipal(t, RoleViewer, "vic")
	if _, err := e.svc.CreateToken(ctx, viewer, TokenInput{Name: "x", Scopes: []Permission{PermUsersWrite}}, nil); codeOf(err) != apperr.CodeInvalid {
		t.Fatalf("viewer granted users.write: %v", err)
	}

	// Token authentication and effective permissions.
	tp, err := e.svc.AuthenticateToken(ctx, main.Secret, "192.0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	if !tp.Can(PermUsersWrite) || tp.Can(PermTokensDelegate) || tp.IsRoot() {
		t.Fatalf("token perms %v root=%v", tp.Permissions(), tp.IsRoot())
	}
	// A root-created token cannot mint non-expiring or delegating tokens.
	if _, err := e.svc.CreateToken(ctx, tp, TokenInput{Name: "child", Scopes: []Permission{PermVehiclesRead}}, nil); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("token without tokens.delegate minted a token: %v", err)
	}
	deleg, err := e.svc.CreateToken(ctx, e.root, TokenInput{Name: "deleg", Scopes: []Permission{PermTokensOwn, PermTokensDelegate, PermVehiclesRead}, ExpiresInDays: 10}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dp, _ := e.svc.AuthenticateToken(ctx, deleg.Secret, "")
	if _, err := e.svc.CreateToken(ctx, dp, TokenInput{Name: "c", Scopes: []Permission{PermVehiclesRead}, NeverExpires: true, AcknowledgeNeverExpires: true}, nil); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("delegated non-expiring token: %v", err)
	}
	if _, err := e.svc.CreateToken(ctx, dp, TokenInput{Name: "c", Scopes: []Permission{PermVehiclesRead}, ExpiresInDays: 30}, nil); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("child outlives parent: %v", err)
	}
	if _, err := e.svc.CreateToken(ctx, dp, TokenInput{Name: "c", Scopes: []Permission{PermTokensDelegate, PermTokensOwn}, ExpiresInDays: 5}, nil); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("child with delegate: %v", err)
	}
	if _, err := e.svc.CreateToken(ctx, dp, TokenInput{Name: "c", Scopes: []Permission{PermUsersWrite}, ExpiresInDays: 5}, nil); codeOf(err) != apperr.CodeInvalid {
		t.Fatalf("child widened scopes: %v", err)
	}
	if _, err := e.svc.CreateToken(ctx, dp, TokenInput{Name: "c", Scopes: []Permission{PermVehiclesRead}, ExpiresInDays: 5}, nil); err != nil {
		t.Fatalf("legit child: %v", err)
	}

	// Self-revocation needs confirmation; revocation is immediate.
	if err := e.svc.RevokeToken(ctx, tp, tp.Token.ID, "", false); codeOf(err) != apperr.CodeConflict {
		t.Fatalf("self revoke without confirm: %v", err)
	}
	if err := e.svc.RevokeToken(ctx, admin, tp.Token.ID, "", false); codeOf(err) != apperr.CodeForbidden {
		t.Fatalf("admin revoked root token: %v", err)
	}
	if err := e.svc.RevokeToken(ctx, e.root, main.ID, "rotated", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AuthenticateToken(ctx, main.Secret, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked token accepted: %v", err)
	}
	// Expiry.
	e.clock.Advance(91 * 24 * time.Hour)
	if _, err := e.svc.AuthenticateToken(ctx, def.Secret, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token accepted: %v", err)
	}
}

func TestTokenFollowsOwner(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin, au := e.userPrincipal(t, RoleAdmin, "alice")
	tok, err := e.svc.CreateToken(ctx, admin, TokenInput{Name: "a", Scopes: []Permission{PermUsersRead, PermVehiclesRead}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	viewer := RoleViewer
	if _, err := e.svc.UpdateUser(ctx, e.root, au.ID, 0, UserInput{Role: &viewer}); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.AuthenticateToken(ctx, tok.Secret, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Can(PermUsersRead) || !p.Can(PermVehiclesRead) {
		t.Fatalf("demoted owner's token kept permissions: %v", p.Permissions())
	}
	suspended := "suspended"
	if _, err := e.svc.UpdateUser(ctx, e.root, au.ID, 0, UserInput{Status: &suspended}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AuthenticateToken(ctx, tok.Secret, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("suspended owner's token accepted: %v", err)
	}
	if err := e.svc.DeleteUser(ctx, e.root, au.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AuthenticateToken(ctx, tok.Secret, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("deleted owner's token accepted: %v", err)
	}
}

func TestTokenParsing(t *testing.T) {
	for _, bad := range []string{"", "wgp_at_", "wgp_at_short_x", "Bearer wgp_at_x", strings.Repeat("a", 80)} {
		if _, _, ok := ParseToken(bad); ok {
			t.Fatalf("parsed %q", bad)
		}
	}
}

func TestTokenUsageFlush(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok, _ := e.svc.CreateToken(ctx, e.root, TokenInput{Name: "u", Scopes: []Permission{PermVehiclesRead}}, nil)
	if _, err := e.svc.AuthenticateToken(ctx, tok.Secret, "192.0.2.77"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.FlushTokenUsage(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetToken(ctx, e.root, tok.ID)
	if err != nil || got.LastUsedAt == nil || got.LastUsedIP != "192.0.2.77" {
		t.Fatalf("usage %+v %v", got, err)
	}
}
