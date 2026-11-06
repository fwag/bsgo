# 0003: PostgreSQL for Metadata and In-Process libsolv for Scheduling

We decided to store all package dependency metadata (Provides, Requires, Conflicts, filelists) in a relational PostgreSQL database and run openSUSE's `libsolv` SAT solver directly within the Go Scheduler process via CGO.

This decouples dependency computation from slow object store operations. The scheduler can resolve complex multi-package dependency graphs in milliseconds against indexed database tables and in-memory solvable pools, rather than reading binary headers over network filesystems.
