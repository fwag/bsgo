# Issue 07: Specify Access Control, Maintainers, and Notification Routing

Status: closed
Assignee: gemini
Labels: `wayfinder:grilling`

## Question

How should project-level and package-level access control policies (individual maintainers, group maintainers, permissions for modifying manifests or triggering builds, and notification/issue routing to Bugzilla or mailing lists) be declared in YAML schemas, synchronized with Git forges/IAM providers, and enforced by the `bsgo` API service?

## Resolution

Resolved in [docs/specs/04-access-control-and-auth.md](../../docs/specs/04-access-control-and-auth.md) and [docs/adr/0008-database-managed-acls-and-global-package-ownership.md](../../docs/adr/0008-database-managed-acls-and-global-package-ownership.md).
- Authentication: Authentik OIDC integration supporting OAuth2 Device Flow (`bsgo auth login`) for human CLI users, and hashed Personal Access Tokens (PAT) / Service Account Bearer tokens for CI and AI software factory agents.
- Just-In-Time (JIT) user provisioning: Authentik JWTs dynamically populate/update `users` and `groups` in PostgreSQL upon authenticated requests.
- Decoupling ACLs from Git: Project ACLs and maintainers are managed exclusively in PostgreSQL to avoid Git commit churn and merge conflicts when team memberships change.
- Authorization Model:
  1. Superusers (`is_superuser` / Authentik `bsgo-admins` group) have global authority across all projects and workers.
  2. Project Maintainers have full control over a project. Personal forks (`home:<user>`) automatically make the creator maintainer.
  3. Project Readers control visibility for private projects.
- Package Maintainers Out of Scope: Package sources and PR reviews live in external Git forges. Builds are triggered via Git webhooks or dependency cascades, and isolated package rebuilds cannot be triggered without project context. Package-level maintainers are therefore descoped from `bsgo`.
- Notifications & Issue Routing: Explicitly descoped from `bsgo` core; delegated to external webhook receivers.
