package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// API token format (ADR-0014): wgp_at_<12-char lookup>_<43-char base64url secret>. The prefix is stable so secret
// scanners can recognize leaked tokens.
const (
	TokenPrefix     = "wgp_at_"
	tokenLookupLen  = 12
	tokenSecretLen  = 43
	tokenUseFlushes = time.Minute
)

var lookupEnc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Token is the API view of an API token. The secret is never part of it.
type Token struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Description   string       `json:"description"`
	OwnerID       string       `json:"owner_id"`
	Scopes        []Permission `json:"scopes"`
	VehicleIDs    []string     `json:"vehicle_ids"`
	NeverExpires  bool         `json:"never_expires"`
	ExpiresAt     *time.Time   `json:"expires_at,omitempty"`
	Hint          string       `json:"hint" doc:"Non-secret prefix of the token (wgp_at_<lookup>_…) to recognize it."`
	Status        string       `json:"status" enum:"active,expired,revoked"`
	IssuedByToken string       `json:"issued_by_token,omitempty"`
	CreatedBy     string       `json:"created_by"`
	CreatedAt     time.Time    `json:"created_at"`
	LastUsedAt    *time.Time   `json:"last_used_at,omitempty"`
	LastUsedIP    string       `json:"last_used_ip,omitempty"`
	RevokedAt     *time.Time   `json:"revoked_at,omitempty"`
	RevokedReason string       `json:"revoked_reason,omitempty"`
}

func toToken(r db.ApiToken, now time.Time) Token {
	t := Token{ID: r.ID, Name: r.Name, Description: r.Description, OwnerID: r.OwnerID, NeverExpires: r.NeverExpires == 1,
		Hint: TokenPrefix + r.Lookup + "_…", IssuedByToken: r.IssuedByToken.String, CreatedBy: r.CreatedBy,
		CreatedAt: time.UnixMicro(r.CreatedAt).UTC(), LastUsedIP: r.LastUsedIp.String, RevokedReason: r.RevokedReason.String,
		Scopes: []Permission{}, VehicleIDs: []string{}}
	_ = json.Unmarshal([]byte(r.ScopesJson), &t.Scopes)
	_ = json.Unmarshal([]byte(r.VehicleIdsJson), &t.VehicleIDs)
	if r.ExpiresAt.Valid {
		e := time.UnixMicro(r.ExpiresAt.Int64).UTC()
		t.ExpiresAt = &e
	}
	if r.LastUsedAt.Valid {
		l := time.UnixMicro(r.LastUsedAt.Int64).UTC()
		t.LastUsedAt = &l
	}
	t.Status = "active"
	switch {
	case r.RevokedAt.Valid:
		rv := time.UnixMicro(r.RevokedAt.Int64).UTC()
		t.RevokedAt, t.Status = &rv, "revoked"
	case r.ExpiresAt.Valid && now.UnixMicro() >= r.ExpiresAt.Int64:
		t.Status = "expired"
	}
	return t
}

// TokenInput creates an API token.
type TokenInput struct {
	Name          string
	Description   string
	Scopes        []Permission
	VehicleIDs    []string
	ExpiresInDays int // 0 = settings default
	NeverExpires  bool
	// AcknowledgeNeverExpires must be true together with NeverExpires: a non-expiring token is valid until revoked.
	AcknowledgeNeverExpires bool
}

// CreatedToken carries the one-time secret.
type CreatedToken struct {
	Token
	Secret string `json:"token"`
}

// VehicleChecker validates vehicle IDs for token restrictions (installed by the app once vehicles exist).
type VehicleChecker func(ctx context.Context, id string) bool

func (svc *Service) tokenMACFor(lookup, secret string) ([]byte, error) {
	return svc.kr.MAC(keyring.PurposeToken, []byte("wagenpulse/api-token/v1|"+lookup+"|"+secret))
}

// CreateToken mints an API token owned by the caller (ADR-0014). Rules enforced here, not in the UI:
//   - scopes must be known and held by the caller (its current role permissions, or its own token scopes);
//   - a token-authenticated caller needs tokens.delegate and cannot grant tokens.delegate, never-expiring tokens,
//     a later expiry or a wider vehicle restriction than its own token;
//   - only the root user signed in interactively may mint non-expiring tokens, and only with an explicit
//     acknowledgement.
func (svc *Service) CreateToken(ctx context.Context, p *Principal, in TokenInput, vehicleOK VehicleChecker) (*CreatedToken, error) {
	if p == nil || !p.Can(PermTokensOwn) {
		return nil, apperr.New(apperr.CodeForbidden, "missing permission tokens.own")
	}
	v := &apperr.Validation{}
	name := strings.TrimSpace(in.Name)
	if l := len([]rune(name)); l == 0 || l > 100 {
		v.Add("name", "must be 1..100 characters")
	}
	desc := strings.TrimSpace(in.Description)
	if len([]rune(desc)) > 1000 {
		v.Add("description", "must be at most 1000 characters")
	}
	if len(in.Scopes) == 0 {
		v.Add("scopes", "at least one scope is required")
	}
	scopes := NewPermSet()
	for _, s := range in.Scopes {
		switch {
		case !ValidPermission(s):
			v.Add("scopes", "unknown scope %q", s)
		case !p.Can(s):
			v.Add("scopes", "you do not hold %q, so you cannot grant it", s)
		default:
			scopes[s] = struct{}{}
		}
	}
	vehicles := slices.Clone(in.VehicleIDs)
	sort.Strings(vehicles)
	vehicles = slices.Compact(vehicles)
	for _, id := range vehicles {
		if !ids.Valid(ids.Vehicle, id) || (vehicleOK != nil && !vehicleOK(ctx, id)) {
			v.Add("vehicle_ids", "unknown vehicle %q", id)
		}
	}
	sys, err := svc.settings.System(ctx)
	if err != nil {
		return nil, err
	}
	days := in.ExpiresInDays
	if days == 0 && !in.NeverExpires {
		days = sys.Value.TokenDefaultTTLDays
	}
	if in.NeverExpires {
		if days != 0 {
			v.Add("expires_in_days", "must be omitted for a non-expiring token")
		}
		if !in.AcknowledgeNeverExpires {
			v.Add("acknowledge_never_expires", "must be true: a non-expiring token stays valid until it is revoked")
		}
	} else if days < 1 || days > sys.Value.TokenMaxTTLDays {
		v.Add("expires_in_days", "must be within 1..%d", sys.Value.TokenMaxTTLDays)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	if p.MustEnrollTOTP {
		return nil, apperr.New(apperr.CodeForbidden, "enroll TOTP before creating API tokens")
	}
	if in.NeverExpires && !p.IsRoot() {
		return nil, apperr.New(apperr.CodeForbidden, "only the root user, signed in interactively, can create non-expiring tokens")
	}
	now := svc.now()
	var expires *time.Time
	if !in.NeverExpires {
		e := now.Add(time.Duration(days) * 24 * time.Hour)
		expires = &e
	}
	if p.Token != nil {
		if !p.Token.Scopes.Has(PermTokensDelegate) {
			return nil, apperr.New(apperr.CodeForbidden, "this token may not create tokens (missing scope tokens.delegate)")
		}
		if scopes.Has(PermTokensDelegate) {
			return nil, apperr.New(apperr.CodeForbidden, "a token cannot create a token that can create tokens")
		}
		if p.Token.ExpiresAt != nil && expires.After(*p.Token.ExpiresAt) {
			return nil, apperr.New(apperr.CodeForbidden, "a token cannot create a token that outlives it")
		}
		if len(p.Token.VehicleIDs) > 0 {
			if len(vehicles) == 0 {
				return nil, apperr.New(apperr.CodeForbidden, "a vehicle-restricted token can only create tokens restricted to its vehicles")
			}
			for _, id := range vehicles {
				if !slices.Contains(p.Token.VehicleIDs, id) {
					return nil, apperr.New(apperr.CodeForbidden, "a vehicle-restricted token can only create tokens restricted to its vehicles")
				}
			}
		}
	}
	lb := make([]byte, 8)
	if _, err := rand.Read(lb); err != nil {
		return nil, err
	}
	lookup := lookupEnc.EncodeToString(lb)[:tokenLookupLen]
	sb := make([]byte, 32)
	if _, err := rand.Read(sb); err != nil {
		return nil, err
	}
	secret := base64.RawURLEncoding.EncodeToString(sb)
	mac, err := svc.tokenMACFor(lookup, secret)
	if err != nil {
		return nil, err
	}
	scopeList := scopes.Sorted()
	sj, _ := json.Marshal(scopeList)
	vj, _ := json.Marshal(vehicles)
	id := ids.New(ids.APIToken)
	var issuedBy string
	if p.Token != nil {
		issuedBy = p.Token.ID
	}
	err = svc.s.W().TxNamed(ctx, "auth.token_create", func(tx *store.Tx) error {
		ne := int64(0)
		var exp = nullInt(0, false)
		if in.NeverExpires {
			ne = 1
		} else {
			exp = nullInt(expires.UnixMicro(), true)
		}
		if err := tx.Q.InsertAPIToken(ctx, db.InsertAPITokenParams{ID: id, Lookup: lookup, Mac: mac, Name: name, Description: desc,
			OwnerID: p.UserID, ScopesJson: string(sj), VehicleIdsJson: string(vj), NeverExpires: ne, ExpiresAt: exp,
			IssuedByToken: nullStr(issuedBy), CreatedBy: p.AuditActor().ID, CreatedAt: tx.NowMicros()}); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.AuditActor(), Action: "token.created", Target: audit.Target{Type: "api_token", ID: id},
			Details: map[string]any{"name": name, "owner_id": p.UserID, "scopes": scopeList, "vehicle_ids": vehicles,
				"never_expires": in.NeverExpires, "expires_in_days": days, "parent_token_id": issuedBy}})
		return err
	})
	if err != nil {
		return nil, err
	}
	row, err := svc.s.R().GetAPIToken(ctx, id)
	if err != nil {
		return nil, err
	}
	return &CreatedToken{Token: toToken(row, now), Secret: TokenPrefix + lookup + "_" + secret}, nil
}

// ParseToken splits a raw token into lookup and secret.
func ParseToken(raw string) (lookup, secret string, ok bool) {
	rest, found := strings.CutPrefix(raw, TokenPrefix)
	if !found || len(rest) != tokenLookupLen+1+tokenSecretLen || rest[tokenLookupLen] != '_' {
		return "", "", false
	}
	return rest[:tokenLookupLen], rest[tokenLookupLen+1:], true
}

// AuthenticateToken validates a bearer token and returns its principal. Effective permissions are the owner's
// current role permissions intersected with the token's scopes, so changes to the owner apply immediately.
func (svc *Service) AuthenticateToken(ctx context.Context, raw, ip string) (*Principal, error) {
	lookup, secret, ok := ParseToken(raw)
	if !ok {
		return nil, ErrUnauthenticated
	}
	row, err := svc.s.R().GetAPITokenByLookup(ctx, lookup)
	if isNoRows(err) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	macs, err := svc.kr.MACAll(keyring.PurposeToken, []byte("wagenpulse/api-token/v1|"+lookup+"|"+secret))
	if err != nil {
		return nil, err
	}
	match := false
	for _, m := range macs {
		match = match || hmac.Equal(m, row.Mac)
	}
	now := svc.now()
	if !match || row.RevokedAt.Valid || (row.ExpiresAt.Valid && now.UnixMicro() >= row.ExpiresAt.Int64) {
		return nil, ErrUnauthenticated
	}
	u, err := svc.s.R().GetUser(ctx, row.OwnerID)
	if err != nil || u.Status != "active" {
		return nil, ErrUnauthenticated
	}
	t := toToken(row, now)
	grant := &TokenGrant{ID: row.ID, Name: row.Name, Scopes: NewPermSet(t.Scopes...), VehicleIDs: t.VehicleIDs,
		NeverExpires: t.NeverExpires, ExpiresAt: t.ExpiresAt}
	p := principalFor(u, MethodToken)
	p.perms = p.perms.Intersect(grant.Scopes)
	p.Token = grant
	svc.usedMu.Lock()
	svc.used[row.ID] = tokenUse{at: now, ip: ip}
	svc.usedMu.Unlock()
	return p, nil
}

// FlushTokenUsage writes buffered last-used timestamps (called periodically and at shutdown).
func (svc *Service) FlushTokenUsage(ctx context.Context) error {
	svc.usedMu.Lock()
	pending := svc.used
	svc.used = map[string]tokenUse{}
	svc.usedMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	return svc.s.W().TxNamed(ctx, "auth.token_usage", func(tx *store.Tx) error {
		for id, u := range pending {
			ts := u.at.UnixMicro()
			if err := tx.Q.TouchAPIToken(ctx, db.TouchAPITokenParams{LastUsedAt: nullInt(ts, true), LastUsedIp: nullStr(u.ip), ID: id,
				LastUsedAt_2: nullInt(ts, true)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// TokenUsageFlushInterval is how often FlushTokenUsage should run.
const TokenUsageFlushInterval = tokenUseFlushes

// ListTokens lists tokens: the caller's own, or everyone's with tokens.manage when all is true.
func (svc *Service) ListTokens(ctx context.Context, p *Principal, all, includeRevoked bool, after string, limit int) ([]Token, error) {
	params := db.ListAPITokensParams{After: after, Lim: int64(limit)}
	if includeRevoked {
		params.IncludeRevoked = 1
	}
	if !all || !p.Can(PermTokensManage) {
		params.OwnerID = nullStr(p.UserID)
	}
	rows, err := svc.s.R().ListAPITokens(ctx, params)
	if err != nil {
		return nil, err
	}
	now := svc.now()
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, toToken(r, now))
	}
	return out, nil
}

// ErrTokenNotFound is returned for tokens the caller cannot see.
var ErrTokenNotFound = apperr.New(apperr.CodeNotFound, "token not found")

// GetToken returns a token visible to the caller.
func (svc *Service) GetToken(ctx context.Context, p *Principal, id string) (Token, error) {
	r, err := svc.s.R().GetAPIToken(ctx, id)
	if isNoRows(err) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, err
	}
	if r.OwnerID != p.UserID && !p.Can(PermTokensManage) {
		return Token{}, ErrTokenNotFound
	}
	return toToken(r, svc.now()), nil
}

// RevokeToken revokes a token. Owners revoke their own; tokens.manage revokes others' except root-owned tokens
// (only root revokes those). Revoking the credential that authenticates the current request requires confirmSelf.
func (svc *Service) RevokeToken(ctx context.Context, p *Principal, id, reason string, confirmSelf bool) error {
	if p.Token != nil && p.Token.ID == id && !confirmSelf {
		return apperr.New(apperr.CodeConflict, "this request is authenticated by the token being revoked; "+
			"pass confirm_self_revoke=true to revoke it anyway (rotate first: create a replacement token, switch to it, then revoke)")
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return svc.s.W().TxNamed(ctx, "auth.token_revoke", func(tx *store.Tx) error {
		r, err := tx.Q.GetAPIToken(ctx, id)
		if isNoRows(err) {
			return ErrTokenNotFound
		}
		if err != nil {
			return err
		}
		if r.OwnerID != p.UserID {
			if !p.Can(PermTokensManage) {
				return ErrTokenNotFound
			}
			owner, err := tx.Q.GetUser(ctx, r.OwnerID)
			if err == nil && Role(owner.Role) == RoleRoot && !p.IsRoot() {
				return apperr.New(apperr.CodeForbidden, "only the root user can revoke root's tokens")
			}
		}
		if r.RevokedAt.Valid {
			return nil // idempotent
		}
		if _, err := tx.Q.RevokeAPIToken(ctx, db.RevokeAPITokenParams{RevokedAt: nullInt(tx.NowMicros(), true), RevokedBy: nullStr(p.AuditActor().ID),
			RevokedReason: nullStr(reason), ID: id}); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: p.AuditActor(), Action: "token.revoked", Target: audit.Target{Type: "api_token", ID: id},
			Details: map[string]any{"owner_id": r.OwnerID, "name": r.Name, "self": p.Token != nil && p.Token.ID == id}})
		return err
	})
}

// PermissionPresets are scope bundles offered by the UI and documented for automation.
func PermissionPresets() map[string][]Permission {
	terraform := RolePermissions(RoleAdmin)
	terraform = slices.DeleteFunc(terraform, func(p Permission) bool { return p == PermTokensDelegate || p == PermVehiclesPurge })
	return map[string][]Permission{
		"read_only": RolePermissions(RoleViewer),
		"operator":  RolePermissions(RoleOperator),
		"terraform": terraform,
	}
}
