package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth/oidc"
	"github.com/aurimasniekis/go-wagenpulse/internal/buildinfo"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// UserCreate creates a user.
type UserCreate struct {
	Username          string     `json:"username" minLength:"1" maxLength:"64" doc:"Lowercase letters, digits, '.', '_', '-', '@'. Immutable."`
	DisplayName       *string    `json:"display_name,omitempty" maxLength:"120"`
	Email             *string    `json:"email,omitempty" maxLength:"254"`
	Role              *auth.Role `json:"role,omitempty" enum:"admin,operator,viewer" doc:"Default viewer. Only root may create admins."`
	Status            *string    `json:"status,omitempty" enum:"active,suspended"`
	LocalLoginEnabled *bool      `json:"local_login_enabled,omitempty" doc:"Default true."`
	Password          string     `json:"password,omitempty" maxLength:"4096" doc:"Initial local password (write-only, min 12 bytes). Omit for SSO-only users."`
}

// UserUpdate replaces mutable user fields. Omitted fields keep their value.
type UserUpdate struct {
	DisplayName       *string    `json:"display_name,omitempty" maxLength:"120"`
	Email             *string    `json:"email,omitempty" maxLength:"254" doc:"Empty string clears."`
	Role              *auth.Role `json:"role,omitempty" enum:"admin,operator,viewer"`
	Status            *string    `json:"status,omitempty" enum:"active,suspended"`
	LocalLoginEnabled *bool      `json:"local_login_enabled,omitempty"`
	Password          string     `json:"password,omitempty" maxLength:"4096" doc:"Reset the local password (write-only); signs the user out everywhere."`
}

type userOut struct {
	ETag string `header:"ETag"`
	Body auth.User
}

func registerUsers(api huma.API, d *Deps) {
	tags := []string{"users"}
	huma.Register(api, op("list-users", http.MethodGet, "/users", auth.PermUsersRead, "List users", tags...),
		func(ctx context.Context, in *struct{ Pagination }) (*struct{ Body Page[auth.User] }, error) {
			after, err := in.After()
			if err != nil {
				return nil, err
			}
			us, err := d.Auth.ListUsers(ctx, after, in.Lim())
			if err != nil {
				return nil, d.fail(ctx, "list-users", err)
			}
			last := ""
			if len(us) > 0 {
				last = us[len(us)-1].ID
			}
			return &struct{ Body Page[auth.User] }{Body: Page[auth.User]{Items: us, NextCursor: nextCursor(len(us), in.Lim(), last)}}, nil
		})
	huma.Register(api, op("get-user", http.MethodGet, "/users/{user}", auth.PermUsersRead, "Get a user by ID or username", tags...),
		func(ctx context.Context, in *struct {
			User string `path:"user" maxLength:"64"`
		}) (*userOut, error) {
			u, err := d.Auth.FindUser(ctx, in.User)
			if err != nil {
				return nil, d.fail(ctx, "get-user", err)
			}
			return &userOut{ETag: etag(u.Revision), Body: u}, nil
		})
	huma.Register(api, created(op("create-user", http.MethodPost, "/users", auth.PermUsersWrite, "Create a user", tags...)),
		func(ctx context.Context, in *struct{ Body UserCreate }) (*userOut, error) {
			b := in.Body
			u, err := d.Auth.CreateUser(ctx, Principal(ctx), auth.UserInput{Username: b.Username, DisplayName: b.DisplayName, Email: b.Email,
				Role: b.Role, Status: b.Status, LocalLoginEnabled: b.LocalLoginEnabled, Password: b.Password})
			if err != nil {
				return nil, d.fail(ctx, "create-user", err)
			}
			return &userOut{ETag: etag(u.Revision), Body: u}, nil
		})
	huma.Register(api, op("update-user", http.MethodPatch, "/users/{user}", auth.PermUsersWrite, "Update a user", tags...),
		func(ctx context.Context, in *struct {
			User string `path:"user" maxLength:"64"`
			IfMatch
			Body UserUpdate
		}) (*userOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			cur, err := d.Auth.FindUser(ctx, in.User)
			if err != nil {
				return nil, d.fail(ctx, "update-user", err)
			}
			b := in.Body
			u, err := d.Auth.UpdateUser(ctx, Principal(ctx), cur.ID, rev, auth.UserInput{DisplayName: b.DisplayName, Email: b.Email, Role: b.Role,
				Status: b.Status, LocalLoginEnabled: b.LocalLoginEnabled, Password: b.Password})
			if err != nil {
				return nil, d.fail(ctx, "update-user", err)
			}
			return &userOut{ETag: etag(u.Revision), Body: u}, nil
		})
	huma.Register(api, noContent(op("delete-user", http.MethodDelete, "/users/{user}", auth.PermUsersWrite,
		"Delete a user (sessions, identities and API tokens are removed)", tags...)),
		func(ctx context.Context, in *struct {
			User string `path:"user" maxLength:"64"`
		}) (*struct{}, error) {
			cur, err := d.Auth.FindUser(ctx, in.User)
			if err != nil {
				return nil, d.fail(ctx, "delete-user", err)
			}
			if err := d.Auth.DeleteUser(ctx, Principal(ctx), cur.ID); err != nil {
				return nil, d.fail(ctx, "delete-user", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, op("list-user-identities", http.MethodGet, "/users/{user}/identities", auth.PermUsersRead, "A user's linked SSO identities", tags...),
		func(ctx context.Context, in *struct {
			User string `path:"user" maxLength:"64"`
		}) (*struct{ Body []oidc.Identity }, error) {
			u, err := d.Auth.FindUser(ctx, in.User)
			if err != nil {
				return nil, d.fail(ctx, "identities", err)
			}
			ids, err := d.OIDC.Identities(ctx, u.ID)
			if err != nil {
				return nil, d.fail(ctx, "identities", err)
			}
			return &struct{ Body []oidc.Identity }{Body: ids}, nil
		})
	huma.Register(api, noContent(op("unlink-user-identity", http.MethodDelete, "/users/{user}/identities/{id}", auth.PermUsersWrite,
		"Unlink a user's SSO identity", tags...)),
		func(ctx context.Context, in *struct {
			User string `path:"user" maxLength:"64"`
			ID   string `path:"id" maxLength:"64"`
		}) (*struct{}, error) {
			u, err := d.Auth.FindUser(ctx, in.User)
			if err != nil {
				return nil, d.fail(ctx, "unlink", err)
			}
			if u.Role == auth.RoleRoot && !Principal(ctx).IsRoot() {
				return nil, d.fail(ctx, "unlink", apperr.New(apperr.CodeForbidden, "only root can change root's identities"))
			}
			if err := d.OIDC.Unlink(ctx, Principal(ctx), u.ID, in.ID); err != nil {
				return nil, d.fail(ctx, "unlink", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, noContent(op("reset-user-totp", http.MethodDelete, "/users/{user}/totp", auth.PermUsersWrite,
		"Remove a user's TOTP (lost authenticator)", tags...)),
		func(ctx context.Context, in *struct {
			User string `path:"user" maxLength:"64"`
		}) (*struct{}, error) {
			u, err := d.Auth.FindUser(ctx, in.User)
			if err != nil {
				return nil, d.fail(ctx, "reset-totp", err)
			}
			if err := d.Auth.DisableTOTP(ctx, Principal(ctx), u.ID, ""); err != nil {
				return nil, d.fail(ctx, "reset-totp", err)
			}
			return &struct{}{}, nil
		})
}

// TokenCreate creates an API token owned by the caller.
type TokenCreate struct {
	Name                    string            `json:"name" minLength:"1" maxLength:"100"`
	Description             string            `json:"description,omitempty" maxLength:"1000"`
	Scopes                  []auth.Permission `json:"scopes" minItems:"1" maxItems:"64" doc:"Permissions granted to the token; you must hold each one. Effective permissions are always limited by your current role."`
	VehicleIDs              []string          `json:"vehicle_ids,omitempty" maxItems:"100" doc:"Restrict the token to these vehicles (empty = all)."`
	ExpiresInDays           int               `json:"expires_in_days,omitempty" minimum:"0" maximum:"366" doc:"Default from system settings; omit for never_expires."`
	NeverExpires            bool              `json:"never_expires,omitempty" doc:"Root only, interactive only. The token stays valid until revoked; store it in a secret manager and rotate it deliberately."`
	AcknowledgeNeverExpires bool              `json:"acknowledge_never_expires,omitempty" doc:"Must be true with never_expires."`
}

// TokenScopes lists the permission catalogue and presets.
type TokenScopes struct {
	Permissions []auth.Permission            `json:"permissions"`
	Presets     map[string][]auth.Permission `json:"presets"`
}

func registerTokens(api huma.API, d *Deps) {
	tags := []string{"tokens"}
	huma.Register(api, op("list-token-scopes", http.MethodGet, "/token-scopes", auth.PermTokensOwn, "Permission catalogue and scope presets", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body TokenScopes }, error) {
			return &struct{ Body TokenScopes }{Body: TokenScopes{Permissions: auth.AllPermissions, Presets: auth.PermissionPresets()}}, nil
		})
	huma.Register(api, op("list-tokens", http.MethodGet, "/tokens", auth.PermTokensOwn, "List API tokens (yours, or everyone's with all=true and tokens.manage)", tags...),
		func(ctx context.Context, in *struct {
			All            bool `query:"all"`
			IncludeRevoked bool `query:"include_revoked"`
			Pagination
		}) (*struct{ Body Page[auth.Token] }, error) {
			after, err := in.After()
			if err != nil {
				return nil, err
			}
			ts, err := d.Auth.ListTokens(ctx, Principal(ctx), in.All, in.IncludeRevoked, after, in.Lim())
			if err != nil {
				return nil, d.fail(ctx, "list-tokens", err)
			}
			last := ""
			if len(ts) > 0 {
				last = ts[len(ts)-1].ID
			}
			return &struct{ Body Page[auth.Token] }{Body: Page[auth.Token]{Items: ts, NextCursor: nextCursor(len(ts), in.Lim(), last)}}, nil
		})
	huma.Register(api, op("get-token", http.MethodGet, "/tokens/{id}", auth.PermTokensOwn, "Token metadata (never the secret)", tags...),
		func(ctx context.Context, in *struct {
			ID string `path:"id" maxLength:"64"`
		}) (*struct{ Body auth.Token }, error) {
			t, err := d.Auth.GetToken(ctx, Principal(ctx), in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-token", err)
			}
			return &struct{ Body auth.Token }{Body: t}, nil
		})
	huma.Register(api, created(op("create-token", http.MethodPost, "/tokens", auth.PermTokensOwn,
		"Create an API token; the secret is returned only in this response", tags...)),
		func(ctx context.Context, in *struct{ Body TokenCreate }) (*struct{ Body auth.CreatedToken }, error) {
			b := in.Body
			t, err := d.Auth.CreateToken(ctx, Principal(ctx), auth.TokenInput{Name: b.Name, Description: b.Description, Scopes: b.Scopes,
				VehicleIDs: b.VehicleIDs, ExpiresInDays: b.ExpiresInDays, NeverExpires: b.NeverExpires, AcknowledgeNeverExpires: b.AcknowledgeNeverExpires},
				d.vehicleExists)
			if err != nil {
				return nil, d.fail(ctx, "create-token", err)
			}
			return &struct{ Body auth.CreatedToken }{Body: *t}, nil
		})
	huma.Register(api, noContent(op("revoke-token", http.MethodDelete, "/tokens/{id}", auth.PermTokensOwn,
		"Revoke an API token immediately", tags...)),
		func(ctx context.Context, in *struct {
			ID                string `path:"id" maxLength:"64"`
			Reason            string `query:"reason" maxLength:"200"`
			ConfirmSelfRevoke bool   `query:"confirm_self_revoke" doc:"Required when revoking the token that authenticates this request."`
		}) (*struct{}, error) {
			if err := d.Auth.RevokeToken(ctx, Principal(ctx), in.ID, in.Reason, in.ConfirmSelfRevoke); err != nil {
				return nil, d.fail(ctx, "revoke-token", err)
			}
			return &struct{}{}, nil
		})
}

// vehicleExists is replaced once vehicles exist (domain wiring).
func (d *Deps) vehicleExists(ctx context.Context, id string) bool {
	if d.Store == nil {
		return false
	}
	var n int
	err := d.Store.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM vehicles WHERE id = ?`, id).Scan(&n)
	return err == nil && n == 1
}

type systemSettingsOut struct {
	ETag string `header:"ETag"`
	Body settings.System
}

type retentionOut struct {
	ETag string `header:"ETag"`
	Body settings.Retention
}

func registerSettings(api huma.API, d *Deps) {
	tags := []string{"settings"}
	huma.Register(api, op("get-system-settings", http.MethodGet, "/settings/system", auth.PermSettingsRead, "System settings", tags...),
		func(ctx context.Context, _ *struct{}) (*systemSettingsOut, error) {
			doc, err := d.Settings.System(ctx)
			if err != nil {
				return nil, d.fail(ctx, "get-system", err)
			}
			return &systemSettingsOut{ETag: etag(doc.Revision), Body: doc.Value}, nil
		})
	huma.Register(api, op("put-system-settings", http.MethodPut, "/settings/system", auth.PermSettingsWrite,
		"Replace system settings (refused if root would lose every login method)", tags...),
		func(ctx context.Context, in *struct {
			IfMatch
			Body settings.System
		}) (*systemSettingsOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			doc, err := d.Settings.UpdateSystem(ctx, Principal(ctx).AuditActor(), rev, in.Body)
			if err != nil {
				return nil, d.fail(ctx, "put-system", err)
			}
			return &systemSettingsOut{ETag: etag(doc.Revision), Body: doc.Value}, nil
		})
	huma.Register(api, op("reset-system-settings", http.MethodDelete, "/settings/system", auth.PermSettingsWrite, "Restore default system settings", tags...),
		func(ctx context.Context, _ *struct{}) (*systemSettingsOut, error) {
			doc, err := d.Settings.ResetSystem(ctx, Principal(ctx).AuditActor())
			if err != nil {
				return nil, d.fail(ctx, "reset-system", err)
			}
			return &systemSettingsOut{ETag: etag(doc.Revision), Body: doc.Value}, nil
		})
	huma.Register(api, op("get-retention-settings", http.MethodGet, "/settings/retention", auth.PermSettingsRead, "Retention settings", tags...),
		func(ctx context.Context, _ *struct{}) (*retentionOut, error) {
			doc, err := d.Settings.Retention(ctx)
			if err != nil {
				return nil, d.fail(ctx, "get-retention", err)
			}
			return &retentionOut{ETag: etag(doc.Revision), Body: doc.Value}, nil
		})
	huma.Register(api, op("put-retention-settings", http.MethodPut, "/settings/retention", auth.PermSettingsWrite, "Replace retention settings", tags...),
		func(ctx context.Context, in *struct {
			IfMatch
			Body settings.Retention
		}) (*retentionOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			doc, err := d.Settings.UpdateRetention(ctx, Principal(ctx).AuditActor(), rev, in.Body)
			if err != nil {
				return nil, d.fail(ctx, "put-retention", err)
			}
			return &retentionOut{ETag: etag(doc.Revision), Body: doc.Value}, nil
		})
	huma.Register(api, op("reset-retention-settings", http.MethodDelete, "/settings/retention", auth.PermSettingsWrite, "Restore default retention settings", tags...),
		func(ctx context.Context, _ *struct{}) (*retentionOut, error) {
			doc, err := d.Settings.ResetRetention(ctx, Principal(ctx).AuditActor())
			if err != nil {
				return nil, d.fail(ctx, "reset-retention", err)
			}
			return &retentionOut{ETag: etag(doc.Revision), Body: doc.Value}, nil
		})
}

// ProviderBody creates or replaces an OIDC provider.
type ProviderBody struct {
	Slug                 string   `json:"slug,omitempty" maxLength:"40" doc:"Required on create; immutable."`
	DisplayName          string   `json:"display_name" minLength:"1" maxLength:"80"`
	Issuer               string   `json:"issuer" minLength:"1" maxLength:"512"`
	ClientID             string   `json:"client_id" minLength:"1" maxLength:"255"`
	ClientSecret         *string  `json:"client_secret,omitempty" maxLength:"4096" doc:"Write-only. Omit to keep the stored secret; empty string clears it."`
	ClientSecretRef      string   `json:"client_secret_ref,omitempty" maxLength:"512" doc:"External reference instead of a stored secret: env:WAGENPULSE_SECRET_NAME or file:/run/secrets/…"`
	Scopes               []string `json:"scopes,omitempty" maxItems:"20"`
	Enabled              bool     `json:"enabled"`
	Provisioning         string   `json:"provisioning,omitempty" enum:"preprovisioned,jit"`
	DefaultRole          string   `json:"default_role,omitempty" enum:"operator,viewer"`
	AllowedDomains       []string `json:"allowed_domains,omitempty" maxItems:"50"`
	RequireVerifiedEmail bool     `json:"require_verified_email"`
}

func (b ProviderBody) input() oidc.ProviderInput {
	return oidc.ProviderInput{Slug: b.Slug, DisplayName: b.DisplayName, Issuer: b.Issuer, ClientID: b.ClientID, ClientSecret: b.ClientSecret,
		ClientSecretRef: b.ClientSecretRef, Scopes: b.Scopes, Enabled: b.Enabled, Provisioning: b.Provisioning, DefaultRole: b.DefaultRole,
		AllowedDomains: append([]string{}, b.AllowedDomains...), RequireVerifiedEmail: b.RequireVerifiedEmail}
}

type providerOut struct {
	ETag string `header:"ETag"`
	Body oidc.Provider
}

func registerOIDCProviders(api huma.API, d *Deps) {
	tags := []string{"oidc"}
	huma.Register(api, op("list-oidc-providers", http.MethodGet, "/oidc-providers", auth.PermOIDCRead, "List OIDC providers", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []oidc.Provider }, error) {
			ps, err := d.OIDC.List(ctx)
			if err != nil {
				return nil, d.fail(ctx, "list-providers", err)
			}
			return &struct{ Body []oidc.Provider }{Body: ps}, nil
		})
	huma.Register(api, op("get-oidc-provider", http.MethodGet, "/oidc-providers/{provider}", auth.PermOIDCRead, "Get an OIDC provider by ID or slug", tags...),
		func(ctx context.Context, in *struct {
			Provider string `path:"provider" maxLength:"64"`
		}) (*providerOut, error) {
			p, err := d.OIDC.Get(ctx, in.Provider)
			if err != nil {
				return nil, d.fail(ctx, "get-provider", err)
			}
			return &providerOut{ETag: etag(p.Revision), Body: p}, nil
		})
	huma.Register(api, created(op("create-oidc-provider", http.MethodPost, "/oidc-providers", auth.PermOIDCWrite, "Create an OIDC provider", tags...)),
		func(ctx context.Context, in *struct{ Body ProviderBody }) (*providerOut, error) {
			p, err := d.OIDC.Create(ctx, Principal(ctx), in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "create-provider", err)
			}
			return &providerOut{ETag: etag(p.Revision), Body: p}, nil
		})
	huma.Register(api, op("update-oidc-provider", http.MethodPut, "/oidc-providers/{provider}", auth.PermOIDCWrite, "Replace an OIDC provider", tags...),
		func(ctx context.Context, in *struct {
			Provider string `path:"provider" maxLength:"64"`
			IfMatch
			Body ProviderBody
		}) (*providerOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			p, err := d.OIDC.Update(ctx, Principal(ctx), in.Provider, rev, in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "update-provider", err)
			}
			return &providerOut{ETag: etag(p.Revision), Body: p}, nil
		})
	huma.Register(api, noContent(op("delete-oidc-provider", http.MethodDelete, "/oidc-providers/{provider}", auth.PermOIDCWrite,
		"Delete an OIDC provider and unlink its identities", tags...)),
		func(ctx context.Context, in *struct {
			Provider string `path:"provider" maxLength:"64"`
		}) (*struct{}, error) {
			if err := d.OIDC.Delete(ctx, Principal(ctx), in.Provider); err != nil {
				return nil, d.fail(ctx, "delete-provider", err)
			}
			return &struct{}{}, nil
		})
}

// AuditRecord is an audit event as returned by the API.
type AuditRecord struct {
	audit.Record
	AtTime time.Time `json:"at_time"`
}

func registerAudit(api huma.API, d *Deps) {
	huma.Register(api, op("list-audit-events", http.MethodGet, "/audit-events", auth.PermAuditRead, "Search the audit log (newest first)", "audit"),
		func(ctx context.Context, in *struct {
			ActorID      string    `query:"actor_id" maxLength:"64"`
			TargetID     string    `query:"target_id" maxLength:"64"`
			ActionPrefix string    `query:"action" maxLength:"64" doc:"Action prefix, e.g. token. or auth.login"`
			Outcome      string    `query:"outcome" enum:"success,failure,denied,"`
			Since        time.Time `query:"since"`
			Until        time.Time `query:"until"`
			Limit        int       `query:"limit" minimum:"1" maximum:"200" default:"50"`
			BeforeSeq    int64     `query:"before_seq" minimum:"0" doc:"Cursor: return events older than this sequence number."`
		}) (*struct {
			Body struct {
				Items      []AuditRecord `json:"items"`
				NextBefore int64         `json:"next_before_seq,omitempty"`
			}
		}, error) {
			p := db.SearchAuditEventsParams{BeforeSeq: in.BeforeSeq, Since: 0, Until: d.now().Add(time.Hour).UnixMicro(), Lim: int64(max(1, min(in.Limit, 200)))}
			if p.BeforeSeq == 0 {
				p.BeforeSeq = 1 << 62
			}
			if !in.Since.IsZero() {
				p.Since = in.Since.UnixMicro()
			}
			if !in.Until.IsZero() {
				p.Until = in.Until.UnixMicro()
			}
			p.ActorID = nullAny(in.ActorID)
			p.TargetID = nullAny(in.TargetID)
			p.ActionPrefix = nullAny(in.ActionPrefix)
			p.Outcome = nullAny(in.Outcome)
			rows, err := d.Store.R().SearchAuditEvents(ctx, p)
			if err != nil {
				return nil, d.fail(ctx, "audit", err)
			}
			out := &struct {
				Body struct {
					Items      []AuditRecord `json:"items"`
					NextBefore int64         `json:"next_before_seq,omitempty"`
				}
			}{}
			out.Body.Items = make([]AuditRecord, 0, len(rows))
			for _, r := range rows {
				rec := audit.DecodeRecord(r)
				out.Body.Items = append(out.Body.Items, AuditRecord{Record: rec, AtTime: time.UnixMicro(rec.At).UTC()})
			}
			if len(rows) == int(p.Lim) {
				out.Body.NextBefore = rows[len(rows)-1].Seq
			}
			return out, nil
		})
	huma.Register(api, op("verify-audit-chain", http.MethodPost, "/audit-events/verify", auth.PermAuditRead, "Verify the audit hash chain", "audit"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body audit.VerifyResult }, error) {
			res, err := audit.Verify(ctx, d.Store.R(), d.auditAnchor(ctx))
			if err != nil {
				return nil, d.fail(ctx, "audit-verify", err)
			}
			return &struct{ Body audit.VerifyResult }{Body: res}, nil
		})
}

// auditAnchor returns the chain anchor left by retention purges.
func (d *Deps) auditAnchor(ctx context.Context) *audit.Anchor {
	row, err := d.Store.R().GetSetting(ctx, audit.AnchorSettingKey)
	if err != nil {
		return nil
	}
	var a audit.Anchor
	if json.Unmarshal([]byte(row.Value), &a) != nil {
		return nil
	}
	return &a
}

func nullAny(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SystemInfo describes the server for diagnostics and client compatibility checks.
type SystemInfo struct {
	Version        string    `json:"version"`
	APIVersion     string    `json:"api_version"`
	Build          any       `json:"build"`
	InstallationID string    `json:"installation_id,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	Ready          bool      `json:"ready"`
	ReadyError     string    `json:"ready_error,omitempty"`
	SchemaVersion  int64     `json:"schema_version"`
}

func registerSystem(api huma.API, d *Deps) {
	huma.Register(api, public("get-version", http.MethodGet, "/version", "Server and API version (no authentication)", "system"),
		func(ctx context.Context, _ *struct{}) (*struct {
			Body struct {
				Version    string `json:"version"`
				APIVersion string `json:"api_version"`
			}
		}, error) {
			out := &struct {
				Body struct {
					Version    string `json:"version"`
					APIVersion string `json:"api_version"`
				}
			}{}
			out.Body.Version, out.Body.APIVersion = buildinfo.Get().Version, APIVersion
			return out, nil
		})
	huma.Register(api, op("get-system-info", http.MethodGet, "/system/info", auth.PermSystemRead, "Server information", "system"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body SystemInfo }, error) {
			info := SystemInfo{Version: buildinfo.Get().Version, APIVersion: APIVersion, Build: buildinfo.Get(), InstallationID: d.InstallationID,
				StartedAt: d.StartedAt.UTC(), Ready: true}
			if d.Ready != nil {
				if err := d.Ready(ctx); err != nil {
					info.Ready, info.ReadyError = false, err.Error()
				}
			}
			if d.Store != nil {
				_ = d.Store.ReadDB().QueryRowContext(ctx, `SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&info.SchemaVersion)
			}
			return &struct{ Body SystemInfo }{Body: info}, nil
		})
}
