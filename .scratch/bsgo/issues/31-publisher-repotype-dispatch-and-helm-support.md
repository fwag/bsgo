# 31: Publisher: Multi-RepoType Dispatching & Helm Support

**What to build:**
Enable multi-format repository publication in `pkg/publisher/publisher.go` and `cmd/bsgo-publisher` based on declared `RepoTypes`:
1. **Message Contract Extension**:
   - Add `RepoTypes []string` to `v1.PublishRequest` in `pkg/proto/v1/messages.go`.
2. **RepoType Dispatching**:
   - `["rpm-md"]` (Default): Run `createrepo_c` and upload `repodata/` XML metadata atomically to S3.
   - `["none"]`: Bypass `createrepo_c` and metadata upload entirely; publish raw compiled artifacts (ISOs, appliance disk images, raw binaries) to S3 without metadata errors.
   - `["helm"]`: Execute Helm repository indexing (`helm repo index`), generating and uploading `index.yaml` to the target S3 path.
3. **Multi-RepoType Targets**:
   - Support declaring multiple repo types simultaneously on a target (e.g. `repo_types: ["rpm-md", "helm"]`).
4. **Standalone Daemon Integration**:
   - Wire `RepoTypes` handling into the `bsgo-publisher` daemon (`cmd/bsgo-publisher`), ensuring non-RPM publications emit `bsgo.events.published` without failing on missing `repomd.xml`.

**Blocked by:** 23: Publisher: Standalone bsgo-publisher Daemon & Worker Decoupling, 29: Manifest: Project & Target Config Schema, Hierarchy & Merging

**Status:** resolved

- [x] Add `RepoTypes []string` to `v1.PublishRequest` in `pkg/proto/v1/messages.go`.
- [x] Update `ProcessPublishRequest` in `pkg/publisher/publisher.go` to inspect `RepoTypes`.
- [x] Implement bypass logic for `RepoTypes: ["none"]`.
- [x] Implement `HelmIndexExecutor` interface and integration for `RepoTypes: ["helm"]`.
- [x] Wire multi-repo dispatching into `cmd/bsgo-publisher/main.go` and `cmd/bsgo-server/main.go`.
- [x] Add unit tests in `pkg/publisher/publisher_test.go` covering `none`, `rpm-md`, and `helm` paths.
