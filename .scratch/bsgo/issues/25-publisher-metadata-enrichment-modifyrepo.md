# 25: Publisher: Metadata Enrichment via modifyrepo_c (AppStream & Errata/CVE)

**What to build:**
Extend the publisher pipeline with `modifyrepo_c` execution to inject rich application and advisory metadata:
1. **modifyrepo_c Subprocess Wrapper**: Create `ModifyRepoExecutor` in `pkg/publisher/modifyrepo.go` for manipulating generated `repomd.xml`.
2. **Security Advisories & Errata (`updateinfo.xml`)**: Parse package changes, CVE references, and patch metadata to construct `updateinfo.xml`, injecting it via `modifyrepo_c --mdtype=updateinfo`.
3. **AppStream Software Catalog**: Extract AppStream XML metadata (`/usr/share/metainfo/` or `appstream.xml.gz`) and desktop icons from built RPMs and inject them via `modifyrepo_c --mdtype=appstream`.
4. **Signing Re-hash**: Ensure `repomd.xml` hash calculations and detached GPG signatures (`repomd.xml.asc`) occur *after* all `modifyrepo_c` injections have completed.

**Blocked by:** 23: Publisher: Standalone bsgo-publisher Daemon & Worker Decoupling

**Status:** open

- [ ] Implement `ModifyRepoExecutor` in `pkg/publisher/modifyrepo.go`.
- [ ] Generate and inject `updateinfo.xml` containing errata and CVE metadata for `zypper patch` / `zypper cve`.
- [ ] Extract and inject AppStream catalog metadata (`appstream.xml.gz`) for desktop software centers.
- [ ] Verify `repomd.xml` signature reflects post-enrichment checksums.
- [ ] Unit and integration tests verifying `modifyrepo_c` injection and client validation.
