# 0008: Database-Managed Project Access Control and Descoped Package Ownership

We decided to store and manage Access Control Lists (ACLs) exclusively in PostgreSQL at the **Project** level, while making package-level maintainers explicitly **out of scope** for `bsgo`.

Because source code, branch reviews, and patch proposals are decoupled to external Git forges (e.g. Gitea, GitHub) and dedicated SCM services, package-level ownership and contributor permissions belong strictly in those forges. `bsgo` acts as a build bot: builds are triggered automatically by Git webhooks or dependency rebuild cascades. Within `bsgo`, individual package-level maintainers cannot unilaterally trigger isolated package rebuilds without project context.

The simplified authorization model requires only:
1. **Global Superusers**: Cluster-wide administrators (mapped via Authentik `bsgo-admins` group) with universal permissions across all projects, workers, and settings.
2. **Project Maintainers**: Full operational control over a specific Project (managing targets, project configs, and project-wide rebuild triggers).
3. **Project Readers**: Visibility controls for private projects (public projects are readable by all).
