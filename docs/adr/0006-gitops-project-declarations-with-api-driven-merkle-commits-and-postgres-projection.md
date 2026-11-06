# 0006: GitOps Project Declarations with API-Driven Merkle Commits and Postgres Projection

We decided to keep all canonical distribution and package declarations in Git repositories (leveraging Git's Merkle tree for cryptographic auditability, tamper detection, and PR review workflows), while projecting this state into PostgreSQL for high-speed scheduler queries and providing an API-assisted mutation bridge.

Rather than requiring users to manually navigate hash-sharded Git trees (`packages/xx/<package>.yaml`) to update package pins or hardware constraints, the `bsgo` API service performs signed Git commits and pushes on the user's behalf. PostgreSQL serves as the live operational cache, updated via Git webhooks, keeping git trees as the immutable ledger of record without sacrificing CLI responsiveness.
