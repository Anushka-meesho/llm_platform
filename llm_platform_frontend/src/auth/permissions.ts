// Frontend mirror of the backend RBAC model (internal/auth/rbac.go). The
// gateway is the source of truth and enforces every rule; this table only lets
// the Studio UI hide or disable actions the current role can't perform, so a
// user never clicks a button that would 403.

export type Permission =
  | 'task:read'
  | 'task:predict'
  | 'task:write'
  | 'task:deploy'
  | 'task:delete'
  | 'task:view_prompt';

const ROLE_PERMISSIONS: Record<string, Permission[]> = {
  admin: ['task:read', 'task:predict', 'task:write', 'task:deploy', 'task:delete', 'task:view_prompt'],
};

const DEFAULT_ROLE = 'admin';

export function can(role: string | undefined, perm: Permission): boolean {
  const perms = ROLE_PERMISSIONS[role || DEFAULT_ROLE];
  return perms ? perms.includes(perm) : false;
}
