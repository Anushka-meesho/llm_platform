package auth

// Role-based authorization.
//
// The platform's User Journey (see docs "LLM Platform PFS") separates three
// actors: a prompt *creator* (product/analyst/engineer) who authors and
// iterates on tasks and prompts, an *approver* who owns the publish gate
// (Gate 2 — human approval before an endpoint goes live), and the backend
// engineer / service *caller* who only invokes the product predict API and
// never touches prompts. RBAC encodes that separation as capabilities so the
// gateway can enforce it uniformly across every route.
//
// Roles are carried inside the signed JWT (see Claims.Role), so the gateway
// authorizes from the token alone — no per-request identity-store lookup, which
// keeps the prediction hot path free of synchronous DB work.

// Permission is a single capability gated by RBAC.
type Permission string

const (
	// PermTaskRead — read task config, versions, stats, runs, shadow reports.
	PermTaskRead Permission = "task:read"
	// PermTaskPredict — invoke the product prediction endpoint.
	PermTaskPredict Permission = "task:predict"
	// PermTaskWrite — author tasks/prompts: create, update, save drafts,
	// Studio test runs, shadow comparisons.
	PermTaskWrite Permission = "task:write"
	// PermTaskDeploy — the publish gate: activate a prompt version into
	// production. Deliberately distinct from PermTaskWrite so authoring and
	// publishing can be held by different people.
	PermTaskDeploy Permission = "task:deploy"
)

// Defined roles.
const (
	RoleAdmin    = "admin"    // superuser: every capability
	RoleCreator  = "creator"  // prompt creator: author + iterate, but cannot publish
	RoleApprover = "approver" // owns the publish gate: read/predict/deploy
	RoleCaller   = "caller"   // service principal / integration: read + predict only
	RoleViewer   = "viewer"   // view-only share access (PFS step 7)
)

// DefaultRole is assigned when a token carries no role claim — legacy session
// tokens and service principals minted before RBAC. It is the least-privilege
// role that can still call the product predict API and read task config, which
// is exactly what a backend-engineer integration needs, so existing service
// tokens keep working without being re-minted.
const DefaultRole = RoleCaller

var rolePermissions = map[string]map[Permission]bool{
	RoleAdmin:    {PermTaskRead: true, PermTaskPredict: true, PermTaskWrite: true, PermTaskDeploy: true},
	RoleCreator:  {PermTaskRead: true, PermTaskPredict: true, PermTaskWrite: true},
	RoleApprover: {PermTaskRead: true, PermTaskPredict: true, PermTaskDeploy: true},
	RoleCaller:   {PermTaskRead: true, PermTaskPredict: true},
	RoleViewer:   {PermTaskRead: true},
}

// KnownRole reports whether role is a defined role. Used to validate the
// issue-token -role flag at mint time rather than at call time.
func KnownRole(role string) bool {
	_, ok := rolePermissions[role]
	return ok
}

// HasPermission reports whether role grants perm. An empty role is treated as
// DefaultRole; an unknown role grants nothing.
func HasPermission(role string, perm Permission) bool {
	if role == "" {
		role = DefaultRole
	}
	return rolePermissions[role][perm]
}

// Can reports whether the user's role grants perm.
func (u *User) Can(perm Permission) bool {
	if u == nil {
		return false
	}
	return HasPermission(u.Role, perm)
}
