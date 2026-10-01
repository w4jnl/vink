package domain

// Role is a user's role in an org. Roles apply to every project in the org.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleViewer, RoleMember, RoleAdmin, RoleOwner:
		return true
	}
	return false
}

// Level orders roles so "at least member" is a comparison.
func (r Role) Level() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleMember:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	}
	return 0
}

// AtLeast reports whether r grants everything min grants.
func (r Role) AtLeast(min Role) bool { return r.Level() >= min.Level() }

// Access is an API key's access level.
type Access string

const (
	AccessRO Access = "ro"
	AccessRW Access = "rw"
)

// Valid reports whether a is ro or rw.
func (a Access) Valid() bool { return a == AccessRO || a == AccessRW }

// Scope is the authenticated caller's reach. It travels in the request
// context, set once by the auth middleware, and every service method takes
// it as the first argument after ctx.
type Scope struct {
	OrgID     string
	ProjectID string
	// UserID is set for session and proxy callers, empty for API keys.
	UserID string
	// Role is the effective role: the membership role for users, viewer
	// for ro keys and admin for rw keys. Instance admins are owners
	// everywhere.
	Role          Role
	InstanceAdmin bool
	// Actor names the caller for logs and audit: user:<subject> or key:<prefix>.
	Actor string
	// KeyID and KeyAccess are set for API-key callers.
	KeyID     string
	KeyAccess Access
}

// IsKey reports whether the caller is an API key.
func (s Scope) IsKey() bool { return s.KeyID != "" }

// IsOrgKey reports whether the caller is an org key: an API key bound to
// the org and no project, allowed to export and apply the org's projects.
func (s Scope) IsOrgKey() bool { return s.KeyID != "" && s.ProjectID == "" }

// CanOperate: ack incidents, pause/resume, check now.
func (s Scope) CanOperate() bool { return s.Role.AtLeast(RoleMember) }

// CanEdit: create/edit/delete monitors, channels, routes, maintenance,
// pages; apply.
func (s Scope) CanEdit() bool { return s.Role.AtLeast(RoleMember) }

// CanSeePingKey: see the ping key; create ro API keys.
func (s Scope) CanSeePingKey() bool { return s.Role.AtLeast(RoleMember) }

// CanAdminProject: rotate the ping key, create/revoke rw keys, project
// settings, delete the project.
func (s Scope) CanAdminProject() bool { return s.Role.AtLeast(RoleAdmin) }

// CanAdminOrg: create projects, manage members and agents.
func (s Scope) CanAdminOrg() bool { return s.Role.AtLeast(RoleAdmin) }

// CanOwnOrg: transfer ownership, delete the org.
func (s Scope) CanOwnOrg() bool { return s.Role.AtLeast(RoleOwner) }
