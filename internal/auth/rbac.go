// Package auth implements WagenPulse authentication and authorization: users and roles, local password login with
// throttling and optional TOTP, server-side sessions with CSRF protection, first-run bootstrap, API tokens and the
// permission model every API operation is checked against (ADR-0010, ADR-0011, ADR-0014).
package auth

import (
	"slices"
	"sort"
)

// Role is a user's role.
type Role string

// Roles, from most to least privileged. Exactly one root exists; it is created at bootstrap.
const (
	RoleRoot     Role = "root"
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// AssignableRoles are the roles the API may assign. Root is never assignable.
var AssignableRoles = []Role{RoleAdmin, RoleOperator, RoleViewer}

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool {
	return r == RoleRoot || slices.Contains(AssignableRoles, r)
}

// Rank orders roles (higher is more privileged).
func (r Role) Rank() int {
	switch r {
	case RoleRoot:
		return 4
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// Permission is a fine-grained capability. Every API operation requires exactly one.
type Permission string

// Permissions.
const (
	PermVehiclesRead     Permission = "vehicles.read"
	PermVehiclesWrite    Permission = "vehicles.write"
	PermVehiclesPurge    Permission = "vehicles.purge"
	PermSourcesRead      Permission = "sources.read"
	PermSourcesWrite     Permission = "sources.write"
	PermImportsRead      Permission = "imports.read"
	PermImportsWrite     Permission = "imports.write"
	PermTelemetryRead    Permission = "telemetry.read"
	PermTelemetryExport  Permission = "telemetry.export"
	PermSignalsRead      Permission = "signals.read"
	PermDictionaryWrite  Permission = "dictionary.write"
	PermRulesRead        Permission = "rules.read"
	PermRulesWrite       Permission = "rules.write"
	PermAdaptersRead     Permission = "adapters.read"
	PermAdaptersWrite    Permission = "adapters.write"
	PermRoutesRead       Permission = "routes.read"
	PermRoutesWrite      Permission = "routes.write"
	PermEventsRead       Permission = "events.read"
	PermDeliveriesRetry  Permission = "deliveries.retry"
	PermDeliveriesReplay Permission = "deliveries.replay"
	PermUsersRead        Permission = "users.read"
	PermUsersWrite       Permission = "users.write"
	PermTokensOwn        Permission = "tokens.own"      // list, create and revoke one's own tokens
	PermTokensManage     Permission = "tokens.manage"   // list and revoke every user's tokens
	PermTokensDelegate   Permission = "tokens.delegate" // token-authenticated requests may mint tokens (never implied)
	PermOIDCRead         Permission = "oidc.read"
	PermOIDCWrite        Permission = "oidc.write"
	PermSettingsRead     Permission = "settings.read"
	PermSettingsWrite    Permission = "settings.write"
	PermAuditRead        Permission = "audit.read"
	PermSystemRead       Permission = "system.read"
)

// AllPermissions lists every permission, sorted.
var AllPermissions = func() []Permission {
	ps := []Permission{
		PermVehiclesRead, PermVehiclesWrite, PermVehiclesPurge, PermSourcesRead, PermSourcesWrite, PermImportsRead,
		PermImportsWrite, PermTelemetryRead, PermTelemetryExport, PermSignalsRead, PermDictionaryWrite, PermRulesRead,
		PermRulesWrite, PermAdaptersRead, PermAdaptersWrite, PermRoutesRead, PermRoutesWrite, PermEventsRead,
		PermDeliveriesRetry, PermDeliveriesReplay, PermUsersRead, PermUsersWrite, PermTokensOwn, PermTokensManage,
		PermTokensDelegate, PermOIDCRead, PermOIDCWrite, PermSettingsRead, PermSettingsWrite, PermAuditRead, PermSystemRead,
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i] < ps[j] })
	return ps
}()

// ValidPermission reports whether p is known.
func ValidPermission(p Permission) bool {
	_, ok := slices.BinarySearch(AllPermissions, p)
	return ok
}

var viewerPerms = []Permission{
	PermVehiclesRead, PermSourcesRead, PermImportsRead, PermTelemetryRead, PermTelemetryExport, PermSignalsRead,
	PermRulesRead, PermAdaptersRead, PermRoutesRead, PermEventsRead, PermTokensOwn, PermSystemRead,
}

var operatorPerms = append(slices.Clone(viewerPerms),
	PermVehiclesWrite, PermSourcesWrite, PermImportsWrite, PermDictionaryWrite, PermRulesWrite, PermAdaptersWrite,
	PermRoutesWrite, PermDeliveriesRetry, PermDeliveriesReplay,
)

var adminPerms = append(slices.Clone(operatorPerms),
	PermVehiclesPurge, PermUsersRead, PermUsersWrite, PermTokensManage, PermTokensDelegate, PermOIDCRead, PermOIDCWrite,
	PermSettingsRead, PermSettingsWrite, PermAuditRead,
)

// RolePermissions returns the permissions a role grants. Root holds every permission; root-only actions (minting
// non-expiring tokens, managing admins) are separate checks on the principal, never permissions that could be
// delegated through token scopes.
func RolePermissions(r Role) []Permission {
	switch r {
	case RoleRoot:
		return slices.Clone(AllPermissions)
	case RoleAdmin:
		return slices.Clone(adminPerms)
	case RoleOperator:
		return slices.Clone(operatorPerms)
	case RoleViewer:
		return slices.Clone(viewerPerms)
	}
	return nil
}

// PermSet is a set of permissions.
type PermSet map[Permission]struct{}

// NewPermSet builds a set.
func NewPermSet(ps ...Permission) PermSet {
	s := PermSet{}
	for _, p := range ps {
		s[p] = struct{}{}
	}
	return s
}

// Has reports membership.
func (s PermSet) Has(p Permission) bool {
	_, ok := s[p]
	return ok
}

// Intersect returns s ∩ o.
func (s PermSet) Intersect(o PermSet) PermSet {
	out := PermSet{}
	for p := range s {
		if o.Has(p) {
			out[p] = struct{}{}
		}
	}
	return out
}

// Sorted returns the permissions in order.
func (s PermSet) Sorted() []Permission {
	out := make([]Permission, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Delegable reports whether a permission may be placed in an API token's scopes. tokens.delegate is delegable only
// by sessions (a token can never mint a token that can mint tokens).
func Delegable(p Permission) bool { return ValidPermission(p) }
