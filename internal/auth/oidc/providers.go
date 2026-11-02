// Package oidc implements OpenID Connect single sign-on (ADR-0012): providers are runtime configuration managed
// through the API/UI/Terraform; the login flow uses PKCE S256, a nonce and single-use server-side state; identities are
// keyed by (provider, subject) and linked explicitly, never by email.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/secretref"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// CallbackPath is the single redirect URI for every provider (the provider is resolved from the state).
const CallbackPath = "/auth/oidc/callback"

// Service manages providers and runs logins.
type Service struct {
	s         *store.Store
	kr        *keyring.Keyring
	audit     *audit.Writer
	auth      *auth.Service
	up        *Upstream
	refs      *secretref.Resolver
	publicURL string
	// AllowHTTPIssuers permits http:// issuers (development and tests only).
	AllowHTTPIssuers bool
	log              *slog.Logger
	now              func() time.Time
}

// New builds the service.
func New(s *store.Store, kr *keyring.Keyring, a *audit.Writer, au *auth.Service, up *Upstream, refs *secretref.Resolver,
	publicURL string, log *slog.Logger, now func() time.Time) *Service {
	if log == nil {
		log = obs.Discard()
	}
	if now == nil {
		now = time.Now
	}
	return &Service{s: s, kr: kr, audit: a, auth: au, up: up, refs: refs, publicURL: strings.TrimSuffix(publicURL, "/"),
		log: obs.Component(log, "oidc"), now: now}
}

// Provider is the API view of a provider. The client secret is never returned; SecretFingerprint changes when it does.
type Provider struct {
	ID                   string    `json:"id"`
	Slug                 string    `json:"slug"`
	DisplayName          string    `json:"display_name"`
	Issuer               string    `json:"issuer"`
	ClientID             string    `json:"client_id"`
	ClientSecretRef      string    `json:"client_secret_ref,omitempty"`
	ClientSecretSet      bool      `json:"client_secret_set"`
	SecretFingerprint    string    `json:"secret_fingerprint,omitempty"`
	Scopes               []string  `json:"scopes"`
	Enabled              bool      `json:"enabled"`
	Provisioning         string    `json:"provisioning" enum:"preprovisioned,jit"`
	DefaultRole          string    `json:"default_role" enum:"operator,viewer"`
	AllowedDomains       []string  `json:"allowed_domains"`
	RequireVerifiedEmail bool      `json:"require_verified_email"`
	RedirectURI          string    `json:"redirect_uri"`
	Revision             int64     `json:"revision"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func (svc *Service) toProvider(r db.OidcProvider) Provider {
	p := Provider{ID: r.ID, Slug: r.Slug, DisplayName: r.DisplayName, Issuer: r.Issuer, ClientID: r.ClientID,
		ClientSecretRef: r.ClientSecretRef.String, ClientSecretSet: len(r.ClientSecretEnc) > 0 || r.ClientSecretRef.Valid,
		SecretFingerprint: r.SecretFingerprint, Enabled: r.Enabled == 1, Provisioning: r.Provisioning, DefaultRole: r.DefaultRole,
		RequireVerifiedEmail: r.RequireVerifiedEmail == 1, RedirectURI: svc.publicURL + CallbackPath, Revision: r.Revision,
		CreatedAt: time.UnixMicro(r.CreatedAt).UTC(), UpdatedAt: time.UnixMicro(r.UpdatedAt).UTC(), Scopes: []string{}, AllowedDomains: []string{}}
	_ = json.Unmarshal([]byte(r.ScopesJson), &p.Scopes)
	_ = json.Unmarshal([]byte(r.AllowedDomainsJson), &p.AllowedDomains)
	return p
}

// ProviderInput creates or replaces a provider. Secret handling: ClientSecret (write-only) or ClientSecretRef; on
// update, a nil ClientSecret keeps the stored secret, an empty string clears it.
type ProviderInput struct {
	Slug                 string
	DisplayName          string
	Issuer               string
	ClientID             string
	ClientSecret         *string
	ClientSecretRef      string
	Scopes               []string
	Enabled              bool
	Provisioning         string
	DefaultRole          string
	AllowedDomains       []string
	RequireVerifiedEmail bool
}

var slugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
var domainRE = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func (svc *Service) validate(in *ProviderInput) error {
	v := &apperr.Validation{}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !slugRE.MatchString(in.Slug) {
		v.Add("slug", "must be 1..40 lowercase letters, digits and inner hyphens")
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if l := len([]rune(in.DisplayName)); l == 0 || l > 80 {
		v.Add("display_name", "must be 1..80 characters")
	}
	u, err := url.Parse(in.Issuer)
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		v.Add("issuer", "must be an absolute URL without credentials, query or fragment")
	case u.Scheme != "https" && (u.Scheme != "http" || !svc.AllowHTTPIssuers):
		v.Add("issuer", "must use https")
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	if in.ClientID == "" || len(in.ClientID) > 255 {
		v.Add("client_id", "must be 1..255 characters")
	}
	if in.ClientSecret != nil && *in.ClientSecret != "" && in.ClientSecretRef != "" {
		v.Add("client_secret_ref", "set either client_secret or client_secret_ref, not both")
	}
	if in.ClientSecret != nil && len(*in.ClientSecret) > 4096 {
		v.Add("client_secret", "must be at most 4096 bytes")
	}
	if in.ClientSecretRef != "" {
		if err := svc.refs.Validate(in.ClientSecretRef); err != nil {
			v.Add("client_secret_ref", "%s", err.Error())
		}
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []string{"openid", "profile", "email"}
	}
	if !slices.Contains(in.Scopes, "openid") {
		v.Add("scopes", `must include "openid"`)
	}
	for _, s := range in.Scopes {
		if s == "" || len(s) > 100 || strings.ContainsAny(s, " \t\n") {
			v.Add("scopes", "invalid scope %q", s)
		}
	}
	switch in.Provisioning {
	case "":
		in.Provisioning = "preprovisioned"
	case "preprovisioned", "jit":
	default:
		v.Add("provisioning", "must be preprovisioned or jit")
	}
	switch in.DefaultRole {
	case "":
		in.DefaultRole = string(auth.RoleViewer)
	case string(auth.RoleViewer), string(auth.RoleOperator):
	default:
		v.Add("default_role", "must be viewer or operator (JIT users never become admin or root)")
	}
	for i, d := range in.AllowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		in.AllowedDomains[i] = d
		if !domainRE.MatchString(d) {
			v.Add("allowed_domains", "invalid domain %q", d)
		}
	}
	if in.Provisioning == "jit" && len(in.AllowedDomains) == 0 {
		v.Add("allowed_domains", "just-in-time provisioning requires at least one allowed email domain")
	}
	return v.Err()
}

// ErrProviderNotFound is returned for unknown providers.
var ErrProviderNotFound = apperr.New(apperr.CodeNotFound, "OIDC provider not found")

// List returns every provider.
func (svc *Service) List(ctx context.Context) ([]Provider, error) {
	rows, err := svc.s.R().ListOIDCProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Provider, 0, len(rows))
	for _, r := range rows {
		out = append(out, svc.toProvider(r))
	}
	return out, nil
}

// Get returns a provider by ID or slug.
func (svc *Service) Get(ctx context.Context, idOrSlug string) (Provider, error) {
	r, err := svc.get(ctx, svc.s.R(), idOrSlug)
	if err != nil {
		return Provider{}, err
	}
	return svc.toProvider(r), nil
}

func (svc *Service) get(ctx context.Context, q *db.Queries, idOrSlug string) (db.OidcProvider, error) {
	var r db.OidcProvider
	var err error
	if ids.Valid(ids.OIDCProvider, idOrSlug) {
		r, err = q.GetOIDCProvider(ctx, idOrSlug)
	} else {
		r, err = q.GetOIDCProviderBySlug(ctx, idOrSlug)
	}
	if errors.Is(err, errNoRows) {
		return r, ErrProviderNotFound
	}
	return r, err
}

func (svc *Service) sealSecret(id, secret string) ([]byte, string, error) {
	if secret == "" {
		return nil, "", nil
	}
	sealed, err := svc.kr.Seal(keyring.PurposeField, keyring.FieldAAD("oidc_providers", "client_secret_enc", id), []byte(secret))
	if err != nil {
		return nil, "", err
	}
	fpKey, err := svc.kr.MAC(keyring.PurposeToken, []byte("wagenpulse/fingerprint/v1"))
	if err != nil {
		return nil, "", err
	}
	return sealed, secretref.Fingerprint(fpKey, []byte(secret)), nil
}

// Create adds a provider.
func (svc *Service) Create(ctx context.Context, actor *auth.Principal, in ProviderInput) (Provider, error) {
	if err := svc.validate(&in); err != nil {
		return Provider{}, err
	}
	id := ids.New(ids.OIDCProvider)
	var secret string
	if in.ClientSecret != nil {
		secret = *in.ClientSecret
	}
	sealed, fp, err := svc.sealSecret(id, secret)
	if err != nil {
		return Provider{}, err
	}
	if in.ClientSecretRef != "" {
		fp = "ref"
	}
	sj, _ := json.Marshal(in.Scopes)
	dj, _ := json.Marshal(in.AllowedDomains)
	err = svc.s.W().TxNamed(ctx, "oidc.provider_create", func(tx *store.Tx) error {
		if _, err := tx.Q.GetOIDCProviderBySlug(ctx, in.Slug); err == nil {
			return apperr.New(apperr.CodeAlreadyExists, "a provider with slug %q already exists", in.Slug)
		}
		if err := tx.Q.InsertOIDCProvider(ctx, db.InsertOIDCProviderParams{ID: id, Slug: in.Slug, DisplayName: in.DisplayName, Issuer: in.Issuer,
			ClientID: in.ClientID, ClientSecretEnc: sealed, ClientSecretRef: nullStr(in.ClientSecretRef), SecretFingerprint: fp,
			ScopesJson: string(sj), Enabled: b2i(in.Enabled), Provisioning: in.Provisioning, DefaultRole: in.DefaultRole,
			AllowedDomainsJson: string(dj), RequireVerifiedEmail: b2i(in.RequireVerifiedEmail), CreatedAt: tx.NowMicros(), UpdatedAt: tx.NowMicros()}); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "oidc.provider.created",
			Target: audit.Target{Type: "oidc_provider", ID: id}, Details: map[string]any{"slug": in.Slug, "issuer": in.Issuer,
				"provisioning": in.Provisioning, "enabled": in.Enabled}})
		return err
	})
	if err != nil {
		return Provider{}, err
	}
	return svc.Get(ctx, id)
}

// Update replaces a provider's configuration (ifRevision 0 = unconditional). The slug is immutable.
func (svc *Service) Update(ctx context.Context, actor *auth.Principal, id string, ifRevision int64, in ProviderInput) (Provider, error) {
	cur, err := svc.get(ctx, svc.s.R(), id)
	if err != nil {
		return Provider{}, err
	}
	in.Slug = cur.Slug
	if err := svc.validate(&in); err != nil {
		return Provider{}, err
	}
	sealed, fp, ref := cur.ClientSecretEnc, cur.SecretFingerprint, in.ClientSecretRef
	switch {
	case ref != "":
		sealed, fp = nil, "ref"
	case in.ClientSecret != nil:
		if sealed, fp, err = svc.sealSecret(cur.ID, *in.ClientSecret); err != nil {
			return Provider{}, err
		}
	case cur.ClientSecretRef.Valid: // switching from a reference to "keep" means no secret
		sealed, fp = nil, ""
	}
	sj, _ := json.Marshal(in.Scopes)
	dj, _ := json.Marshal(in.AllowedDomains)
	err = svc.s.W().TxNamed(ctx, "oidc.provider_update", func(tx *store.Tx) error {
		rev := cur.Revision
		if ifRevision != 0 {
			rev = ifRevision
		}
		n, err := tx.Q.UpdateOIDCProvider(ctx, db.UpdateOIDCProviderParams{DisplayName: in.DisplayName, Issuer: in.Issuer, ClientID: in.ClientID,
			ClientSecretEnc: sealed, ClientSecretRef: nullStr(ref), SecretFingerprint: fp, ScopesJson: string(sj), Enabled: b2i(in.Enabled),
			Provisioning: in.Provisioning, DefaultRole: in.DefaultRole, AllowedDomainsJson: string(dj),
			RequireVerifiedEmail: b2i(in.RequireVerifiedEmail), UpdatedAt: tx.NowMicros(), ID: cur.ID, Revision: rev})
		if err != nil {
			return err
		}
		if n != 1 {
			return apperr.New(apperr.CodePrecondition, "the provider was changed by someone else; reload and retry")
		}
		if !in.Enabled {
			if err := auth.CheckRootLoginTx(ctx, tx); err != nil {
				return err
			}
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "oidc.provider.updated",
			Target: audit.Target{Type: "oidc_provider", ID: cur.ID}, Details: map[string]any{"issuer": in.Issuer, "enabled": in.Enabled,
				"provisioning": in.Provisioning, "secret_changed": fp != cur.SecretFingerprint}})
		return err
	})
	if err != nil {
		return Provider{}, err
	}
	svc.up.Forget(cur.ID)
	return svc.Get(ctx, cur.ID)
}

// Delete removes a provider and every identity linked through it.
func (svc *Service) Delete(ctx context.Context, actor *auth.Principal, id string) error {
	cur, err := svc.get(ctx, svc.s.R(), id)
	if err != nil {
		return err
	}
	err = svc.s.W().TxNamed(ctx, "oidc.provider_delete", func(tx *store.Tx) error {
		if _, err := tx.Q.DeleteOIDCProvider(ctx, cur.ID); err != nil {
			return err
		}
		if err := auth.CheckRootLoginTx(ctx, tx); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "oidc.provider.deleted",
			Target: audit.Target{Type: "oidc_provider", ID: cur.ID}, Details: map[string]any{"slug": cur.Slug}})
		return err
	})
	if err == nil {
		svc.up.Forget(cur.ID)
	}
	return err
}

// upstreamConfig builds the upstream client configuration, resolving the client secret.
func (svc *Service) upstreamConfig(r db.OidcProvider) (Config, error) {
	cfg := Config{ID: r.ID, Issuer: r.Issuer, ClientID: r.ClientID, RedirectURL: svc.publicURL + CallbackPath}
	_ = json.Unmarshal([]byte(r.ScopesJson), &cfg.Scopes)
	switch {
	case r.ClientSecretRef.Valid:
		b, err := svc.refs.Resolve(r.ClientSecretRef.String)
		if err != nil {
			return cfg, apperr.Wrap(apperr.CodeUnavailable, err, "the provider's client secret reference cannot be resolved")
		}
		cfg.Secret = strings.TrimSpace(string(b))
	case len(r.ClientSecretEnc) > 0:
		b, err := svc.kr.Open(keyring.PurposeField, keyring.FieldAAD("oidc_providers", "client_secret_enc", r.ID), r.ClientSecretEnc)
		if err != nil {
			return cfg, err
		}
		cfg.Secret = string(b)
	}
	return cfg, nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
