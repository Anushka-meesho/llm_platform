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
	// PermTaskDelete — destructive removal (e.g. pruning prompt versions).
	// Admin-only: it's irreversible and isn't part of the normal author/publish
	// loop.
	PermTaskDelete Permission = "task:delete"
	// PermTaskViewPrompt — see the prompt text itself (template + system prompt
	// + version history bodies). Held by everyone who works ON tasks; withheld
	// from service callers, who integrate against the task contract (schema +
	// metadata) and per the PFS "never touch prompts". A caller still gets task
	// config, schemas, and its own outputs — just not the prompt internals.
	PermTaskViewPrompt Permission = "task:view_prompt"
)

// Defined roles. Two principals exist: the human operator (admin) who runs the
// platform via the Studio, and the service caller (client) — a backend that only
// invokes the product predict API and never touches prompts.
const (
	RoleAdmin  = "admin"  // superuser: every capability (Studio operators)
	RoleClient = "client" // service principal: invoke predict only; never sees prompts
)

// DefaultRole is assigned when a token carries no role claim. It stays admin for
// backward compatibility with existing tokens; issue-token always sets an
// explicit role, so the default only applies to hand-crafted/legacy tokens.
const DefaultRole = RoleAdmin

var rolePermissions = map[string]map[Permission]bool{
	RoleAdmin: {PermTaskRead: true, PermTaskPredict: true, PermTaskWrite: true, PermTaskDeploy: true, PermTaskDelete: true, PermTaskViewPrompt: true},
	// Client: a backend service. Reads the task contract (config/schema/metadata)
	// and invokes predict; deliberately NOT granted view_prompt, write, deploy,
	// or delete — per the PFS, callers integrate against the contract, not prompts.
	RoleClient: {PermTaskRead: true, PermTaskPredict: true},
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
