# 14: Security: Authentik OIDC, JIT User Provisioning, and Scoped Agent Tokens

**What to build:**
Implement the security and access control subsystem. The API gateway verifies Authentik OIDC JWT signatures statelessly using JWKS. Users log in interactively via OAuth2 Device Authorization Flow (`bsgo auth login`). Valid tokens trigger Just-In-Time (JIT) user provisioning in PostgreSQL (`users`, `groups`). CI pipelines and autonomous AI agents authenticate via Personal Access Tokens (PAT) hashed with SHA-256 and restricted to fine-grained scopes (`<action>:<resource>[:<id>]`). Project maintainers and readers are enforced on all API mutation and build trigger endpoints.

**Blocked by:** 08: Foundation: Project and Package Ingestion Gateway with CLI

**Status:** closed

- [x] Authentik OIDC JWT validator in `bsgo-api` verifying signatures against JWKS endpoints.
- [x] CLI command `bsgo auth login` implementing OAuth2 Device Authorization Grant (RFC 8628).
- [x] JIT user and group synchronizer populating the PostgreSQL `users` table upon authenticated requests.
- [x] Personal Access Token manager issuing hashed tokens (`bsgo_pat_...`) with fine-grained scopes (e.g. `project:write:home:bot`).
- [x] Authorization middleware enforcing `is_superuser`, `project_maintainer`, and `project_reader` rules on all API routes.
- [x] Automated security test suite verifying token generation, scope enforcement, blast-radius containment, and unauthorized access rejection.
