# 0002: S3-Compatible Storage for Artifacts and Repodata

We decided to use an S3-compatible object store as the exclusive storage layer for compiled RPM binaries, build logs, and published RPM repository metadata (`repodata/`), completely eliminating the shared POSIX filesystem requirement.

This allows `bsgo` to run statelessly on Kubernetes without `ReadWriteMany` volumes. Standard RPM repositories are generated directly into S3 using `createrepo_c` and served to package managers (`zypper`/`dnf`) via an HTTP reverse-proxy or CDN.
