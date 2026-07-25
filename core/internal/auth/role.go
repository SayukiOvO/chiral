package auth

// Roles are coarse on purpose: three of them, each a superset of the next.
// Per-resource permissions would be more flexible and much easier to get
// subtly wrong, and this panel has one team, not an org chart.
type Role string

const (
	// RoleSuperadmin can do everything, including managing admins.
	RoleSuperadmin Role = "superadmin"
	// RoleOperator runs the fleet: nodes, profiles, variables, users.
	RoleOperator Role = "operator"
	// RoleViewer can look but not touch.
	RoleViewer Role = "viewer"
)

// Roles lists them from most to least privileged, for the UI's picker.
func Roles() []Role { return []Role{RoleSuperadmin, RoleOperator, RoleViewer} }

func ValidRole(r Role) bool {
	switch r {
	case RoleSuperadmin, RoleOperator, RoleViewer:
		return true
	}
	return false
}

// rank orders the roles so a check is a comparison rather than a list of
// cases; anything unknown ranks below viewer and can therefore do nothing.
func rank(r Role) int {
	switch r {
	case RoleSuperadmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// Identity is who is making a request.
type Identity struct {
	// ID and Name are empty for the environment token, which has no account
	// behind it; Name is then a fixed label so the audit trail still says
	// something truthful about where the action came from.
	ID   string
	Name string
	Role Role
	// ViaToken marks the break-glass environment token rather than a session.
	ViaToken bool
}

// EnvTokenIdentity is what the CHIRAL_ADMIN_TOKEN acts as: full rights, and
// clearly labelled in the audit log so "who did this" never silently means
// "somebody with the env token".
func EnvTokenIdentity() Identity {
	return Identity{Name: "env-token", Role: RoleSuperadmin, ViaToken: true}
}

// Can reports whether this identity holds at least the given role.
func (i Identity) Can(min Role) bool { return rank(i.Role) >= rank(min) }

// CanWrite is the common case: anything that changes state.
func (i Identity) CanWrite() bool { return i.Can(RoleOperator) }

// CanAdmin covers managing other admins.
func (i Identity) CanAdmin() bool { return i.Can(RoleSuperadmin) }
