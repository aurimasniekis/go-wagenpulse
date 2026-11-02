package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/mail"
	"strings"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// UserInput creates or updates a user. Nil pointers mean "unchanged" on update.
type UserInput struct {
	Username          string
	DisplayName       *string
	Email             *string
	Role              *Role
	Status            *string
	LocalLoginEnabled *bool
	// Password sets a local password (create, or admin reset on update). Empty means none/unchanged.
	Password string
}

// ErrNotFound is returned for unknown users.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "user not found")

// canManage enforces the role hierarchy: root manages everyone but root; admins manage operators and viewers and may
// not create, promote to or modify admins (only root can).
func canManage(actor *Principal, target Role, newRole Role) error {
	if target == RoleRoot || newRole == RoleRoot {
		return apperr.New(apperr.CodeForbidden, "the root user cannot be created or modified through this operation")
	}
	if actor.Role == RoleRoot && actor.Token == nil {
		return nil
	}
	if target == RoleAdmin || newRole == RoleAdmin {
		return apperr.New(apperr.CodeForbidden, "only the root user, signed in interactively, can manage administrators")
	}
	return nil
}

// GetUser returns a user.
func (svc *Service) GetUser(ctx context.Context, id string) (User, error) {
	u, err := svc.s.R().GetUser(ctx, id)
	if isNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return ToUser(u), nil
}

// FindUser resolves an ID or username.
func (svc *Service) FindUser(ctx context.Context, idOrName string) (User, error) {
	if ids.Valid(ids.User, idOrName) {
		return svc.GetUser(ctx, idOrName)
	}
	u, err := svc.s.R().GetUserByUsername(ctx, strings.ToLower(idOrName))
	if isNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return ToUser(u), nil
}

// ListUsers returns a page of users ordered by ID.
func (svc *Service) ListUsers(ctx context.Context, after string, limit int) ([]User, error) {
	rows, err := svc.s.R().ListUsers(ctx, db.ListUsersParams{After: after, Lim: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, ToUser(r))
	}
	return out, nil
}

func validateProfile(v *apperr.Validation, display, email string) (string, string) {
	display = strings.TrimSpace(display)
	if l := len([]rune(display)); l == 0 || l > 120 {
		v.Add("display_name", "must be 1..120 characters")
	}
	email = strings.TrimSpace(email)
	if email != "" {
		if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
			v.Add("email", "must be a plain email address")
		}
	}
	return display, email
}

// CreateUser creates a user. Usernames are unique (case-insensitive).
func (svc *Service) CreateUser(ctx context.Context, actor *Principal, in UserInput) (User, error) {
	v := &apperr.Validation{}
	name, err := NormalizeUsername(in.Username)
	if err != nil {
		v.Add("username", "%s", err.Error())
	}
	role := RoleViewer
	if in.Role != nil {
		role = *in.Role
	}
	if !ValidRole(role) || role == RoleRoot {
		v.Add("role", "must be admin, operator or viewer")
	}
	display := name
	if in.DisplayName != nil {
		display = *in.DisplayName
	}
	email := ""
	if in.Email != nil {
		email = *in.Email
	}
	display, email = validateProfile(v, display, email)
	status := "active"
	if in.Status != nil {
		status = *in.Status
	}
	if status != "active" && status != "suspended" {
		v.Add("status", "must be active or suspended")
	}
	local := true
	if in.LocalLoginEnabled != nil {
		local = *in.LocalLoginEnabled
	}
	var hash string
	if in.Password != "" {
		pw, err := NormalizePassword(in.Password, MinPasswordBytes)
		if err != nil {
			v.Add("password", "%s", err.Error())
		} else if hash, err = HashPassword(pw, svc.opts.Params); err != nil {
			return User{}, err
		}
	}
	if err := v.Err(); err != nil {
		return User{}, err
	}
	if err := canManage(actor, RoleViewer, role); err != nil {
		return User{}, err
	}
	id := ids.New(ids.User)
	err = svc.s.W().TxNamed(ctx, "auth.user_create", func(tx *store.Tx) error {
		if _, err := tx.Q.GetUserByUsername(ctx, name); err == nil {
			return apperr.New(apperr.CodeAlreadyExists, "a user named %q already exists", name)
		} else if !isNoRows(err) {
			return err
		}
		var changed = nullInt(0, false)
		if hash != "" {
			changed = nullInt(tx.NowMicros(), true)
		}
		lle := int64(0)
		if local {
			lle = 1
		}
		if err := tx.Q.InsertUser(ctx, db.InsertUserParams{ID: id, Username: name, DisplayName: display, Email: nullStr(email), Role: string(role),
			Status: status, PasswordHash: nullStr(hash), PasswordChangedAt: changed, LocalLoginEnabled: lle, CreatedAt: tx.NowMicros(),
			UpdatedAt: tx.NowMicros()}); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "user.created", Target: audit.Target{Type: "user", ID: id},
			Details: map[string]any{"username": name, "role": string(role), "status": status, "password_set": hash != ""}})
		return err
	})
	if err != nil {
		return User{}, err
	}
	return svc.GetUser(ctx, id)
}

// UpdateUser changes a user's profile, role, status or local login, optionally conditioned on revision.
// Demotion, suspension and disabling local login revoke the user's sessions; demotion and suspension apply to API
// tokens immediately because token permissions are computed from the owner's current role (ADR-0014).
func (svc *Service) UpdateUser(ctx context.Context, actor *Principal, id string, ifRevision int64, in UserInput) (User, error) {
	var hash string
	if in.Password != "" {
		pw, err := NormalizePassword(in.Password, MinPasswordBytes)
		if err != nil {
			return User{}, apperr.New(apperr.CodeInvalid, "password: %s", err.Error())
		}
		if hash, err = HashPassword(pw, svc.opts.Params); err != nil {
			return User{}, err
		}
	}
	err := svc.s.W().TxNamed(ctx, "auth.user_update", func(tx *store.Tx) error {
		u, err := tx.Q.GetUser(ctx, id)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ifRevision != 0 && ifRevision != u.Revision {
			return apperr.New(apperr.CodePrecondition, "the user was changed by someone else; reload and retry")
		}
		role := Role(u.Role)
		if in.Role != nil {
			role = *in.Role
		}
		selfRoot := actor.IsRoot() && u.ID == actor.UserID && Role(u.Role) == RoleRoot
		if !selfRoot {
			if err := canManage(actor, Role(u.Role), role); err != nil {
				return err
			}
		} else if role != RoleRoot {
			return apperr.New(apperr.CodeForbidden, "the root user cannot be demoted")
		}
		v := &apperr.Validation{}
		display, email := u.DisplayName, u.Email.String
		if in.DisplayName != nil {
			display = *in.DisplayName
		}
		if in.Email != nil {
			email = *in.Email
		}
		display, email = validateProfile(v, display, email)
		if !ValidRole(role) {
			v.Add("role", "must be admin, operator or viewer")
		}
		status := u.Status
		if in.Status != nil {
			status = *in.Status
		}
		if status != "active" && status != "suspended" {
			v.Add("status", "must be active or suspended")
		}
		if selfRoot && status != "active" {
			v.Add("status", "the root user cannot be suspended")
		}
		local := u.LocalLoginEnabled
		if in.LocalLoginEnabled != nil {
			local = 0
			if *in.LocalLoginEnabled {
				local = 1
			}
		}
		if err := v.Err(); err != nil {
			return err
		}
		n, err := tx.Q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{DisplayName: display, Email: nullStr(email), Role: string(role),
			Status: status, LocalLoginEnabled: local, UpdatedAt: tx.NowMicros(), ID: u.ID, Revision: u.Revision})
		if err != nil {
			return err
		}
		if n != 1 {
			return apperr.New(apperr.CodePrecondition, "the user was changed concurrently; retry")
		}
		if hash != "" {
			if err := tx.Q.SetUserPassword(ctx, db.SetUserPasswordParams{PasswordHash: nullStr(hash), PasswordChangedAt: nullInt(tx.NowMicros(), true),
				UpdatedAt: tx.NowMicros(), ID: u.ID}); err != nil {
				return err
			}
		}
		weakened := Role(u.Role).Rank() > role.Rank() || (u.Status == "active" && status != "active") ||
			(u.LocalLoginEnabled == 1 && local == 0) || hash != ""
		if weakened {
			except := ""
			if u.ID == actor.UserID {
				except = actor.SessionID
			}
			if err := revokeUserSessionsTx(ctx, tx, u.ID, except, "account_changed"); err != nil {
				return err
			}
		}
		if u.Role == string(RoleRoot) {
			if err := CheckRootLoginTx(ctx, tx); err != nil {
				return err
			}
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "user.updated", Target: audit.Target{Type: "user", ID: u.ID},
			Details: map[string]any{"role_from": u.Role, "role_to": string(role), "status_from": u.Status, "status_to": status,
				"local_login_enabled": local == 1, "password_reset": hash != ""}})
		return err
	})
	if err != nil {
		return User{}, err
	}
	return svc.GetUser(ctx, id)
}

// DeleteUser deletes a non-root user; sessions, identities and API tokens go with it.
func (svc *Service) DeleteUser(ctx context.Context, actor *Principal, id string) error {
	return svc.s.W().TxNamed(ctx, "auth.user_delete", func(tx *store.Tx) error {
		u, err := tx.Q.GetUser(ctx, id)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := canManage(actor, Role(u.Role), Role(u.Role)); err != nil {
			return err
		}
		if u.ID == actor.UserID {
			return apperr.New(apperr.CodeForbidden, "you cannot delete your own account")
		}
		if _, err := tx.Q.DeleteUser(ctx, id); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "user.deleted", Target: audit.Target{Type: "user", ID: id},
			Details: map[string]any{"username": u.Username, "role": u.Role}})
		return err
	})
}

// ChangeOwnPassword changes the caller's password after verifying the current one (if any).
func (svc *Service) ChangeOwnPassword(ctx context.Context, p *Principal, current, next string) error {
	if p.Token != nil {
		return apperr.New(apperr.CodeForbidden, "password changes need an interactive sign-in")
	}
	pw, err := NormalizePassword(next, MinPasswordBytes)
	if err != nil {
		return apperr.New(apperr.CodeInvalid, "new_password: %s", err.Error())
	}
	u, err := svc.s.R().GetUser(ctx, p.UserID)
	if err != nil {
		return err
	}
	if u.PasswordHash.Valid && u.PasswordHash.String != "" {
		cur, _ := NormalizePassword(current, 1)
		ok, _, _ := VerifyPassword(cur, u.PasswordHash.String, svc.opts.Params)
		if !ok {
			return apperr.New(apperr.CodeUnauthorized, "the current password is wrong")
		}
	}
	hash, err := HashPassword(pw, svc.opts.Params)
	if err != nil {
		return err
	}
	return svc.s.W().TxNamed(ctx, "auth.password_change", func(tx *store.Tx) error {
		if err := tx.Q.SetUserPassword(ctx, db.SetUserPasswordParams{PasswordHash: nullStr(hash), PasswordChangedAt: nullInt(tx.NowMicros(), true),
			UpdatedAt: tx.NowMicros(), ID: p.UserID}); err != nil {
			return err
		}
		if err := revokeUserSessionsTx(ctx, tx, p.UserID, p.SessionID, "password_changed"); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.AuditActor(), Action: "user.password_changed", Target: audit.Target{Type: "user", ID: p.UserID}})
		return err
	})
}

// TOTPEnrollment is returned when starting TOTP enrollment.
type TOTPEnrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// BeginTOTP stores a new (not yet enabled) TOTP secret for the caller and returns it for the authenticator app.
func (svc *Service) BeginTOTP(ctx context.Context, p *Principal) (TOTPEnrollment, error) {
	if p.Token != nil {
		return TOTPEnrollment{}, apperr.New(apperr.CodeForbidden, "TOTP enrollment needs an interactive sign-in")
	}
	u, err := svc.s.R().GetUser(ctx, p.UserID)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	if u.TotpEnabledAt.Valid {
		return TOTPEnrollment{}, apperr.New(apperr.CodeConflict, "TOTP is already enabled; disable it first")
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return TOTPEnrollment{}, err
	}
	sealed, err := svc.kr.Seal(keyring.PurposeField, keyring.FieldAAD("users", "totp_secret_enc", u.ID), secret)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	err = svc.s.W().TxNamed(ctx, "auth.totp_begin", func(tx *store.Tx) error {
		return tx.Q.SetUserTOTP(ctx, db.SetUserTOTPParams{TotpSecretEnc: sealed, TotpEnabledAt: nullInt(0, false), UpdatedAt: tx.NowMicros(), ID: u.ID})
	})
	if err != nil {
		return TOTPEnrollment{}, err
	}
	return TOTPEnrollment{Secret: EncodeTOTPSecret(secret), URI: TOTPURI(secret, "WagenPulse", u.Username)}, nil
}

const (
	recoveryCodeLen   = 10
	recoveryCodeCount = 10
)

// ConfirmTOTP enables TOTP after a valid code and returns fresh recovery codes (shown once).
func (svc *Service) ConfirmTOTP(ctx context.Context, p *Principal, code string) ([]string, error) {
	u, err := svc.s.R().GetUser(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if len(u.TotpSecretEnc) == 0 || u.TotpEnabledAt.Valid {
		return nil, apperr.New(apperr.CodeConflict, "start TOTP enrollment first")
	}
	secret, err := svc.kr.Open(keyring.PurposeField, keyring.FieldAAD("users", "totp_secret_enc", u.ID), u.TotpSecretEnc)
	if err != nil {
		return nil, err
	}
	step, ok := VerifyTOTP(secret, code, svc.now(), 0)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalid, "the code does not match; check the authenticator app's clock")
	}
	codes := make([]string, recoveryCodeCount)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	for i := range codes {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		codes[i] = strings.ToLower(enc.EncodeToString(b))[:recoveryCodeLen]
	}
	err = svc.s.W().TxNamed(ctx, "auth.totp_confirm", func(tx *store.Tx) error {
		if err := tx.Q.SetUserTOTP(ctx, db.SetUserTOTPParams{TotpSecretEnc: u.TotpSecretEnc, TotpEnabledAt: nullInt(tx.NowMicros(), true),
			UpdatedAt: tx.NowMicros(), ID: u.ID}); err != nil {
			return err
		}
		if _, err := tx.Q.SetUserTOTPStep(ctx, db.SetUserTOTPStepParams{TotpLastStep: step, ID: u.ID, TotpLastStep_2: step}); err != nil {
			return err
		}
		if err := tx.Q.DeleteRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		for _, c := range codes {
			m, err := svc.tokenMAC("recovery", u.ID+"|"+c)
			if err != nil {
				return err
			}
			if err := tx.Q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{UserID: u.ID, CodeMac: m, CreatedAt: tx.NowMicros()}); err != nil {
				return err
			}
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.AuditActor(), Action: "user.totp_enabled", Target: audit.Target{Type: "user", ID: u.ID}})
		return err
	})
	if err != nil {
		return nil, err
	}
	for i, c := range codes {
		codes[i] = c[:5] + "-" + c[5:]
	}
	return codes, nil
}

// DisableTOTP removes TOTP for a user. Users disable their own with a current code; root/admins may reset others'.
func (svc *Service) DisableTOTP(ctx context.Context, actor *Principal, userID, code string) error {
	return svc.s.W().TxNamed(ctx, "auth.totp_disable", func(tx *store.Tx) error {
		u, err := tx.Q.GetUser(ctx, userID)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if u.ID == actor.UserID {
			ok, err := svc.checkSecondFactor(ctx, tx, u, code)
			if err != nil {
				return err
			}
			if !ok && u.TotpEnabledAt.Valid {
				return apperr.New(apperr.CodeUnauthorized, "invalid verification code")
			}
		} else if err := canManage(actor, Role(u.Role), Role(u.Role)); err != nil {
			return err
		}
		if err := tx.Q.SetUserTOTP(ctx, db.SetUserTOTPParams{TotpSecretEnc: nil, TotpEnabledAt: nullInt(0, false), UpdatedAt: tx.NowMicros(), ID: u.ID}); err != nil {
			return err
		}
		if err := tx.Q.DeleteRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "user.totp_disabled", Target: audit.Target{Type: "user", ID: u.ID}})
		return err
	})
}

// BreakGlassResetPassword is the host-side recovery path (`wagenpulse admin reset-password`): it sets a new local
// password, enables local login for the user and in system settings, and signs the user out everywhere.
func (svc *Service) BreakGlassResetPassword(ctx context.Context, userID, password string) error {
	pw, err := NormalizePassword(password, MinPasswordBytes)
	if err != nil {
		return err
	}
	hash, err := HashPassword(pw, svc.opts.Params)
	if err != nil {
		return err
	}
	return svc.s.W().TxNamed(ctx, "auth.break_glass", func(tx *store.Tx) error {
		u, err := tx.Q.GetUser(ctx, userID)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.Q.SetUserPassword(ctx, db.SetUserPasswordParams{PasswordHash: nullStr(hash), PasswordChangedAt: nullInt(tx.NowMicros(), true),
			UpdatedAt: tx.NowMicros(), ID: u.ID}); err != nil {
			return err
		}
		if _, err := tx.Q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{DisplayName: u.DisplayName, Email: u.Email, Role: u.Role, Status: "active",
			LocalLoginEnabled: 1, UpdatedAt: tx.NowMicros(), ID: u.ID, Revision: u.Revision + 1}); err != nil {
			return err
		}
		if err := revokeUserSessionsTx(ctx, tx, u.ID, "", "break_glass"); err != nil {
			return err
		}
		sys, err := settings.SystemTx(ctx, tx)
		if err != nil {
			return err
		}
		if !sys.LocalLoginEnabled {
			sys.LocalLoginEnabled = true
			b, _ := json.Marshal(sys)
			if err := tx.Q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settings.KeySystem, Value: string(b), UpdatedAt: tx.NowMicros(),
				UpdatedBy: nullStr("cli")}); err != nil {
				return err
			}
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.CLI, Action: "user.password_break_glass", Target: audit.Target{Type: "user", ID: u.ID}})
		return err
	})
}
