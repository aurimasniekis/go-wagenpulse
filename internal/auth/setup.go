package auth

import (
	"context"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

const setupPrefix = "wgp_setup_"

// SetupStatus tells the UI whether first-run setup is still possible.
type SetupStatus struct {
	Completed bool `json:"completed"`
}

// Status reports whether a root user exists.
func (svc *Service) SetupStatus(ctx context.Context) (SetupStatus, error) {
	_, err := svc.s.R().GetRootUser(ctx)
	if isNoRows(err) {
		return SetupStatus{}, nil
	}
	if err != nil {
		return SetupStatus{}, err
	}
	return SetupStatus{Completed: true}, nil
}

// IssueSetupToken mints a one-time setup token on the host (`wagenpulse admin setup-token`). It fails once root exists.
func (svc *Service) IssueSetupToken(ctx context.Context) (string, time.Time, error) {
	raw, err := randomToken(setupPrefix)
	if err != nil {
		return "", time.Time{}, err
	}
	mac, err := svc.tokenMAC("setup", raw)
	if err != nil {
		return "", time.Time{}, err
	}
	var exp time.Time
	err = svc.s.W().TxNamed(ctx, "auth.setup_token", func(tx *store.Tx) error {
		if _, err := tx.Q.GetRootUser(ctx); err == nil {
			return apperr.New(apperr.CodeConflict, "setup is already complete: the root user exists")
		} else if !isNoRows(err) {
			return err
		}
		exp = tx.Now.Add(svc.opts.SetupTTL)
		id := ids.New(ids.SetupToken)
		if err := tx.Q.InsertSetupToken(ctx, db.InsertSetupTokenParams{ID: id, TokenMac: mac, CreatedAt: tx.NowMicros(), ExpiresAt: exp.UnixMicro()}); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: audit.CLI, Action: "setup.token_issued", Target: audit.Target{Type: "setup_token", ID: id}})
		return err
	})
	return raw, exp, err
}

// SetupInput creates the root user.
type SetupInput struct {
	Token       string
	Username    string
	DisplayName string
	Password    string
}

// CompleteSetup consumes a setup token and creates the root user with a local password, returning a session.
func (svc *Service) CompleteSetup(ctx context.Context, in SetupInput, meta SessionMeta) (*LoginResult, error) {
	v := &apperr.Validation{}
	name, err := NormalizeUsername(in.Username)
	if err != nil {
		v.Add("username", "%s", err.Error())
	}
	display := strings.TrimSpace(in.DisplayName)
	if display == "" {
		display = name
	}
	if len([]rune(display)) > 120 {
		v.Add("display_name", "must be at most 120 characters")
	}
	pw, err := NormalizePassword(in.Password, MinPasswordBytes)
	if err != nil {
		v.Add("password", "%s", err.Error())
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	if t, err := svc.throttled(ctx, ipKey(meta.IP)); err != nil {
		return nil, err
	} else if t {
		return nil, ErrThrottled
	}
	hash, err := HashPassword(pw, svc.opts.Params)
	if err != nil {
		return nil, err
	}
	macs, err := svc.tokenMACs("setup", in.Token)
	if err != nil {
		return nil, err
	}
	var res LoginResult
	var invalid bool
	err = svc.s.W().TxNamed(ctx, "auth.setup", func(tx *store.Tx) error {
		invalid = false
		if _, err := tx.Q.GetRootUser(ctx); err == nil {
			return apperr.New(apperr.CodeConflict, "setup is already complete")
		} else if !isNoRows(err) {
			return err
		}
		var tok db.SetupToken
		found := false
		for _, m := range macs {
			if t, err := tx.Q.GetSetupTokenByMAC(ctx, m); err == nil {
				tok, found = t, true
				break
			}
		}
		if !found || !strings.HasPrefix(in.Token, setupPrefix) {
			invalid = true
			return svc.recordFailure(ctx, tx, ipKey(meta.IP))
		}
		n, err := tx.Q.UseSetupToken(ctx, db.UseSetupTokenParams{UsedAt: nullInt(tx.NowMicros(), true), ID: tok.ID, ExpiresAt: tx.NowMicros()})
		if err != nil {
			return err
		}
		if n != 1 {
			invalid = true
			return svc.recordFailure(ctx, tx, ipKey(meta.IP))
		}
		id := ids.New(ids.User)
		if err := tx.Q.InsertUser(ctx, db.InsertUserParams{ID: id, Username: name, DisplayName: display, Role: string(RoleRoot),
			Status: "active", PasswordHash: nullStr(hash), PasswordChangedAt: nullInt(tx.NowMicros(), true), LocalLoginEnabled: 1,
			CreatedAt: tx.NowMicros(), UpdatedAt: tx.NowMicros()}); err != nil {
			return err
		}
		if _, err := tx.Q.MarkSetupCompleted(ctx, db.MarkSetupCompletedParams{SetupCompletedAt: nullInt(tx.NowMicros(), true), UpdatedAt: tx.NowMicros()}); err != nil {
			return err
		}
		u, err := tx.Q.GetUser(ctx, id)
		if err != nil {
			return err
		}
		s, err := svc.createSessionTx(ctx, tx, u, MethodPassword, "", meta)
		if err != nil {
			return err
		}
		res.Session, res.User = s, ToUser(u)
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: id}, Action: "setup.completed",
			Target: audit.Target{Type: "user", ID: id}, SourceIP: meta.IP, Details: map[string]any{"username": name}})
		return err
	})
	if err != nil {
		return nil, err
	}
	if invalid {
		return nil, apperr.New(apperr.CodeUnauthorized, "the setup token is invalid, expired or already used")
	}
	return &res, nil
}
