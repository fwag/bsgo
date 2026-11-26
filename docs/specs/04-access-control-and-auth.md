# Specification: Access Control and Authentik Integration

This specification defines the authentication mechanism, authorization model, and PostgreSQL database schema for user identities and project maintainers in `bsgo`.

---

## 1. Authentication Architecture (Authentik IAM)

`bsgo` delegates user identity, password policies, multi-factor authentication (MFA), and single sign-on (SSO) to an external or internal **Authentik** instance.

```
┌──────────────┐         1. Device Flow Login (RFC 8628)         ┌──────────────┐
│  Developer   ├────────────────────────────────────────────────►│   Authentik  │
│   (CLI)      │◄────────────────────────────────────────────────┤     IAM      │
└──────┬───────┘         2. Returns Signed JWT (OIDC)            └──────┬───────┘
       │                                                                │
       │ 3. API Request with Bearer JWT                                 │
       │    (or Static Personal Access Token)                           │
       ▼                                                                ▼
┌────────────────────────────────────────────────────────┐       4. Verify JWKS
│                   bsgo API Gateway                     ├─────────────────────┘
│  - Stateless JWT Signature Verification (Authentik)   │
│  - JIT (Just-In-Time) User Provisioning into PostgreSQL│
└──────────────────────────┬─────────────────────────────┘
                           │
                           ▼
┌────────────────────────────────────────────────────────┐
│                   PostgreSQL Database                  │
│             (users, project_maintainers)               │
└────────────────────────────────────────────────────────┘
```

### 1.1. Interactive Human Login (CLI)
- Users execute `bsgo auth login`.
- The CLI initiates an **OAuth2 Device Authorization Grant (RFC 8628)** against Authentik.
- The user completes login in their browser (SSO / 2FA).
- The CLI receives and securely stores an OIDC ID/Access Token in `~/.config/bsgo/auth.json`.

### 1.2. Machine & AI Software Factory Agent Authentication
- CI pipelines and autonomous AI agents authenticate using **Personal Access Tokens (PAT)** or **Authentik Service Account Tokens** passed via `BSGO_TOKEN` environment variable or `--token` flag.
- PAT tokens are prefixed with `bsgo_pat_...`, hashed using SHA-256, and validated against the database.
- **Fine-Grained Least-Privilege Scoping**: To contain the blast radius of autonomous AI software factory runs, tokens enforce resource-level scopes:
  - Format: `<action>:<resource>[:<id>]`
  - Examples:
    - `project:read:*`: Read access across all projects.
    - `project:write:home:ai-agent`: Write/mutation access restricted strictly to personal project `home:ai-agent`.
    - `build:trigger:openSUSE:Factory`: Rebuild trigger authority granted only for `openSUSE:Factory`.
    - `package:resources:update`: Permission to update VM resource constraints.
    - `admin:gc`: Permission to run storage garbage collection (`bsgo admin gc`), which deletes unreachable build repository indexes and superseded artifacts. It is global, not per project; the project-scoped form is `admin:gc:<project>`. The CLI of an operator talks to Postgres and S3 directly with the deployment credentials; an API that exposes GC must require this scope.
    - `admin:release`: Permission to inspect and change the release numbers of package revisions (`bsgo admin release show|recompute|set`). `show` only reads; `recompute` and `set` change the vrev (CI_CNT) of the next builds of a package. The project-scoped form is `admin:release:<project>`, which limits the commands to that project. The CLI of an operator talks to Postgres directly with the deployment credentials; an API that exposes these commands must require this scope.
  - An agent operating on a staging fork cannot modify release distribution projects even if the owning user has permissions.

### 1.3. Just-In-Time (JIT) Provisioning
The `bsgo` API service does not maintain passwords. When a valid JWT arrives:
- It checks if `authentik_sub` exists in the `users` table.
- If not found, it inserts a new record (`id`, `authentik_sub`, `username`, `email`).
- If found, it updates email or username if changed in Authentik.
- Groups listed in the JWT `groups` claim are mapped dynamically to database groups.

---

## 2. Authorization & Ownership Model

In accordance with ADR 0008, all permissions and maintainer assignments are stored in **PostgreSQL**, keeping Git repositories clean of operational ACL churn.

### 2.1. Descoping Package Maintainers
Package sources and code review workflows live in external Git forges (Gitea/GitHub). The build service acts as an automated build bot: builds are triggered by Git webhooks or dependency rebuild cascades. Package maintainers cannot unilaterally trigger isolated package rebuilds without project context. Therefore, **package-level maintainers are out of scope for `bsgo`**.

### 2.2. Role Hierarchy
1. **Superuser (`is_superuser`)**:
   - Cluster-wide administrative authority across all projects, workers, and settings.
   - Mapped to designated Authentik groups (e.g. `bsgo-admins`).
2. **Project Maintainer**:
   - Full authority over a specific project.
   - Can configure project targets, manage package manifests, trigger project rebuilds, and assign project maintainers.
   - Automatically granted to the creator of personal forks (`home:<username>`).
3. **Project Reader**:
   - For private projects, defines who has read access to logs and artifacts (public projects default to open reading).

---

## 3. PostgreSQL Database Schema

```sql
-- 1. Users Table (Populated via JIT Provisioning from Authentik)
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    authentik_sub   VARCHAR(255) UNIQUE NOT NULL,
    username        VARCHAR(64) UNIQUE NOT NULL,
    email           VARCHAR(255) NOT NULL,
    is_superuser    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. Groups Table (Mirrored from Authentik Group Claims)
CREATE TABLE groups (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(64) UNIQUE NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 3. Personal Access Tokens for AI Agents and CI Pipelines
CREATE TABLE personal_access_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            VARCHAR(64) NOT NULL,
    token_hash      VARCHAR(64) NOT NULL UNIQUE, -- SHA-256 of token
    scopes          TEXT[] NOT NULL DEFAULT '{"build", "project:write"}',
    expires_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 4. Project Maintainers (Users and Groups)
CREATE TABLE project_maintainers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_name    VARCHAR(128) NOT NULL,
    user_id         UUID REFERENCES users(id) ON DELETE CASCADE,
    group_id        UUID REFERENCES groups(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_project_owner CHECK (
        (user_id IS NOT NULL AND group_id IS NULL) OR
        (user_id IS NULL AND group_id IS NOT NULL)
    ),
    CONSTRAINT uq_project_user UNIQUE (project_name, user_id),
    CONSTRAINT uq_project_group UNIQUE (project_name, group_id)
);
CREATE INDEX idx_project_maintainers ON project_maintainers(project_name);

-- 5. Project Privacy & Reader Access
CREATE TABLE project_access (
    project_name    VARCHAR(128) PRIMARY KEY,
    is_private      BOOLEAN NOT NULL DEFAULT FALSE,
    allowed_groups  TEXT[] DEFAULT '{}',
    allowed_users   TEXT[] DEFAULT '{}'
);
```

---

## 4. Authorization Decision Resolution Flow

When a user or agent requests to modify project configurations, targets, or trigger project builds:

```
                           ┌─────────────────────────┐
                           │    Incoming Request     │
                           │     (user, project)     │
                           └────────────┬────────────┘
                                        │
                                        ▼
                               /─────────────────\
                              <  Is Superuser?    >────[YES]───► ALLOW
                               \─────────────────/
                                        │ [NO]
                                        ▼
                               /─────────────────\
                              < Project Maintainer>────[YES]───► ALLOW
                               \─────────────────/
                                        │ [NO]
                                        ▼
                                      DENY
```
