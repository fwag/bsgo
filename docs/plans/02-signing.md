# Plan: Signing (RPMs, repository metadata, provenance)

Status: S0-S4 implemented; S5-S6 proposed
Audience: implementation agents (Sonnet) + human reviewer
Depends on: plan 01 (scheduler, indexer, publisher, hermetic worker) — merged.

Follow the rules of plan 01 §0 and §6 (AGENTS.md gates, no MD5, no hardcoded
"standard"/"x86_64", OBS behaviour as the reference, Perl/obs tools only as
test oracles unless a decision below says otherwise).

---

## 1. Current state (verified 2026-11-15)

| Artifact | OBS | bsgo today |
|---|---|---|
| Built RPMs | Signed by `bs_signer` with the project key before they enter `:full` (job state `signing` → `finished`, `BSSched/BuildJob.pm`; `bs_signer:signjob`, `sign -r`) | **Not signed.** `SignerService.SignPackageArtifacts` is never called. Its `SignRPM` would corrupt RPMs: it appends `[BSGO_SIGNED_RPM]` + raw bytes to the file instead of writing the RPM signature header. |
| `repomd.xml.asc` | OpenPGP detached, armored, project key | Raw RSA PKCS#1 signature in a PEM-like wrapper (`pkg/signer/remote_driver.go:SignDetached`). Not OpenPGP: zypper/dnf `gpgcheck` reject it. |
| `repomd.xml.key` / project pubkey | OpenPGP public key (`_pubkey`), also `<project>.key` in the published repo | PEM RSA public key, not OpenPGP. |
| SLSA provenance | DSSE envelope signed by `bs_signer:signslsaprovenance` | Unsigned (`_slsa_provenance.json` written by the worker). |
| Key management | Per project, inherited from the parent namespace unless the project has its own (`bs_signer:getsignkey`); `osc signkey` create/extend/delete; signflavor per prjconf (`BuildFlags: signflavor:...`) | `bsgo-signd` generates an RSA-2048 key pair per project/flavor on first use, encrypts it with a master key, stores it in `project_signing_keys`. No OpenPGP, no inheritance, no expiry, no CLI. |
| Job states | `signing` (`S`), then `finished` (`f`) | Neither exists. |

`bsgo-signd` itself (TCP service, CIDR allow-list, optional mTLS, master-key
encryption, HSM hook) is a sound base. What is wrong is the signature
*formats* and the missing pipeline stage.

## 1a. How legacy OBS does it (source-verified)

Components: `bs_signer` (backend daemon), `bs_publish`, `bs_srcserver`, and the
external obs-sign package: `sign` (client) + `signd` (daemon holding the
instance's master gpg key). `BSConfig::sign` points at the `sign` client.

- **Keys** (`BSSrcServer/Signkey.pm:createkey`, `bs_srcserver:createkey`):
  `sign -P <tmpfile> -g rsa@4096 800 "<project> OBS Project" <project>@<obsname>`.
  Defaults `rsa@4096`, 800 days. Result: `_pubkey` (armored public key) and
  `_signkey` (the project private key **encrypted to signd's master key**),
  both stored as versioned project meta files in the source server. signd
  never stores project keys; it decrypts `_signkey` per request (`-P`).
  `extendkey` = `sign -x <days>`, `getsignkey ... autoextend=1` extends
  automatically when a key is about to expire.
- **Inheritance** (`bs_srcserver:2662`, `getsignkey`): walk up the namespace
  (`a:b:c` → `a:b` → `a`) to the first project with a `_signkey`; that project
  is also the RPM `%vendor obs://<obsname>/<signproject>`. No key anywhere →
  the instance default key (`sign -p`, or `BSConfig::keyfile`).
- **Sign flavors**: prjconf `BuildFlags: signflavor:<name>` selects a named
  key, allowed values from `BSConfig::sign_flavor` (SLFO uses
  `signflavor:2023-05-10` and `2025-01-22` for ppc64le/s390x).
- **RPM signing** (`bs_signer:signjob`): after a successful build the job
  status goes `signing` (`BSSched/BuildJob.pm`); bs_signer fetches the key
  (`getsignkey`), then for every signable file: rpm/drpm →
  `sign --delsign -r` then `sign -r [-T buildtime] -h sha256 -P <signkey>`.
  The `sign` client rewrites the RPM signature header itself and only sends
  the digest to signd. With `-S .checksums` it records the new checksums.
  Also signed: `.sha256/.sha512` (`-d`), iso, pkg.tar.*, AppImage, appx
  (with an SSL cert from the key), helm, apk, and `_slsa_provenance.json`
  (DSSE, `signslsaprovenance`) — provenance last, because its subject
  digests change when the RPMs get signed. Then the job is `finished` and the
  scheduler integrates the signed binaries.
- **Repository metadata** (`bs_publish:883`): `sign -d -4 repodata/repomd.xml`
  writes the armored detached `repomd.xml.asc`; `repomd.xml.key` = the
  project public key; the generated `.repo` file has
  `gpgkey=<url>repodata/repomd.xml.key`.

Mapping to bsgo: `bsgo-signd` already plays signd's role (master key, HSM
hook, network ACL). OBS stores the encrypted project keys in the source
server; bsgo stores them in `project_signing_keys` (Postgres), also encrypted
with the master key — same trust model. `bs_signer` becomes the `signer`
role (S3). The `sign` client's RPM header rewriting is replaced by `rpmsign`
plus a helper that asks bsgo-signd to sign the digest (SD2).

## 2. Decisions

**Decided 2026-11-15:** SD1 = (a) OpenPGP in Go inside `bsgo-signd`
(ProtonMail/go-crypto); SD2 = (a) `rpmsign --addsign` with `bsgo-sign-helper`
forwarding digests to bsgo-signd; SD3 = OBS defaults (RSA-4096, 800 days,
autoextend on use).

- **SD1. OpenPGP implementation.**
  - (a) Recommended: OpenPGP in Go inside `bsgo-signd` using
    `github.com/ProtonMail/go-crypto/openpgp` (actively maintained successor of
    `x/crypto/openpgp`, used by GitHub, Proton, Go module mirrors; passes
    AGENTS §2). Keys never leave signd; signd produces OpenPGP v4 signature
    packets over digests sent by clients (same split as obs-signd: the client
    hashes, the daemon signs the digest).
  - (b) Alternative: run obs-signd + gpg as the signing daemon and talk its
    wire protocol. Battle-tested, but adds a C daemon + gpg-agent to the
    control plane and loses the existing master-key/HSM design.
- **SD2. RPM signing engine.**
  - (a) Recommended: `rpmsign --addsign` on the signing node with
    `%__gpg_sign_cmd` pointing at a tiny bsgo helper (`bsgo-sign-helper`)
    that forwards the digest to `bsgo-signd` and prints the OpenPGP signature.
    rpm writes the signature header itself (v4/v6 rpm formats, RSA/EdDSA,
    header-only vs header+payload) — the format is rpm's, not ours.
  - (b) Alternative: write the RPM signature header in Go. More code, every
    rpm format change becomes our bug. Not recommended.
- **SD3. Key algorithm and lifetime.** Recommended: OBS defaults, RSA-4096
  OpenPGP v4 keys with 800 days expiry (`Signkey.pm`), automatic extension on
  use like `getsignkey autoextend=1`, manual extend via CLI. EdDSA only later
  (rpm 4.20+). Existing RSA-2048 raw keys are
  migrated by generating new OpenPGP keys (no published consumers exist yet).

## 3. Target design

```
worker ──job finished (unsigned RPMs + bininfo, sha256 of unsigned files)──▶ NATS
                                                                              │
              bsgo-signer (new role of bsgo-server, horizontally scalable,     │
              NATS queue group; no ambient keys, talks to bsgo-signd)  ◀──────┘
                 state: building → signing
                 1. download the job's RPMs from S3
                 2. rpmsign --addsign (helper → bsgo-signd, project key)
                 3. verify (rpm -K with the project public key)
                 4. re-extract bininfo (sha256 changes; hdr data does not)
                 5. upload signed RPMs + bininfo.json (replace unsigned)
                 6. sign _slsa_provenance.json as DSSE (in-toto)
                 7. emit job-signed
              state: signing → finished
indexer ◀──job-signed── integrates (as today), state finished → succeeded
publisher: repomd.xml.asc (OpenPGP armored detached) + repomd.xml.key
           (OpenPGP armored pubkey), plus <project>.key in the repo root
```

Rules:
- Binary identity (plan 01 §6) is the sha256 of the **signed** file: only
  signed binaries enter the build repository, so bdeps, meta and published
  repodata all refer to the same bytes. The worker's unsigned sha256 is only
  used to verify the download in step 1.
- Signing must be idempotent per job ID (crash between steps re-runs the job's
  signing; already signed RPMs are re-signed deterministically or skipped when
  `rpm -K` already verifies with the current key).
- Projects without a key (signing disabled for the project) skip `signing`
  and go `building → finished` directly, like OBS.
- The worker never sees keys (SLSA: no ambient credentials on build hosts).

## 4. Work packages

### S0. Remove the fake implementations (small, first) - DONE
Delete `SignRPM`/`SignPackageArtifacts` byte-appending code and the PEM
"OpenPGP-looking" detached format; make the publisher skip `repomd.xml.asc`
until S2 lands (no signature is better than an invalid one). Tests updated.
Acceptance: no code path writes non-OpenPGP signatures.

### S1. OpenPGP keys in bsgo-signd (SD1, SD3) - DONE (protocol and client; signflavor selection from prjconf arrives with S3)

Implemented as documented in `docs/specs/05-signd-protocol.md`: stateless signd with a two-phase digest protocol (TRAILER, SIGN), `pkg/signer` client with namespace inheritance and autoextend, migration 00008. The publisher has no signer until S2.
Files: `pkg/signd` (`crypto_engine.go`, `master_key.go`, `server.go`),
`pkg/storage` (`project_signing_keys`: add `fingerprint`, `algo`,
`expires_at`, `armored_pubkey`; migration), `pkg/signer`.
Tasks: generate OpenPGP v4 primary keys (signing capable) with user ID
`<project> OBS Project <project@bsgo>` (configurable template), encrypted at
rest with the master key as today; protocol commands `GENKEY`, `PUBKEY`
(armored), `SIGN <keyid> <hashalgo> <hexdigest> <sigtype> <created>` returning
an OpenPGP signature packet (binary or armored on request); `EXTEND <keyid>
<days>`. Keep CIDR allow-list and mTLS. Key inheritance: lookup project →
parent namespace (`a:b:c` → `a:b` → `a`) like `bs_signer:getsignkey`; signflavor
from the project config `BuildFlags: signflavor:<f>` selects a named key.
Tests: table-driven; verify produced signatures with `gpg --verify` (oracle,
build tag `oracle`) and with go-crypto's verifier (normal CI).

### S2. Repository metadata signing (publisher) - DONE

Implemented: `signer.RepoSigner` (`pkg/signer/service.go`), server flags `--sign-repos`, `--sign-create-keys`, `--repo-download-url`, `<project>.repo` per repository (`pkg/publisher/repofile.go`). As in OBS bs_publish, only `repodata/repomd.xml.asc` and `repomd.xml.key` are published; a separate `<project>.key` is not written (the `.repo` file references `repomd.xml.key`). Verified with go-crypto in CI and with `gpg --verify` and `zypper ar --gpgcheck-strict` + `ref` (and tamper rejection) in `test/oracle/repomd_signing_test.go`.
Files: `pkg/publisher/subrepo.go`, `pkg/signer/service.go`.
Tasks: `repomd.xml.asc` = armored OpenPGP detached signature (sigtype binary
document, SHA-256) from signd; `repomd.xml.key` and `<repo root>/repodata/...`
plus `<project>.key` at the repository root = armored public key. Upload order
stays: data, then signature + key, then `repomd.xml` last.
Acceptance (integration, podman): `zypper --root /tmp/t ar --gpgcheck-strict
<repo>`, import the key, `zypper ref` succeeds; tampering with `repomd.xml`
makes `zypper ref` fail. Same with `dnf --setopt=gpgcheck=1 makecache` if
available.

### S3. RPM signing stage (SD2) - DONE (provenance signing is S4)

Implemented: `pkg/signing` (stage, `RPMTool`, helper logic), `cmd/bsgo-sign-helper`,
server role `signer` (`cmd/bsgo-server/signing.go`, part of `all`, flags
`--sign-rpms`, `--sign-create-keys` (shared with S2), `--sign-helper`,
`--sign-workers`, `--sign-ack-wait`, `--sign-max-deliver`),
migration 00009 (`signing`, `finished`, `dispatching`), `jobstatus.HandleSigned`.
Contract in docs/specs/02 (signing stage) and docs/specs/05 (helper). rpm 4.20
interface: `%__gpg` override, plaintext `-` is stdin. Signature time =
max(RPM build time, key creation). Open: sign flavor from prjconf (always
`default` for now), non-RPM artifacts (ISO, checksums) are passed through
unsigned. Requests and results are JetStream work queues with durable
consumers (ack after upload and publish, heartbeats, MaxDeliver then failed).

Original work package text:
Files: new `pkg/signing` (stage logic), new `cmd/bsgo-sign-helper` (gpg
replacement for rpm's `%__gpg_sign_cmd`; reads data on stdin, hashes,
calls signd `SIGN`, writes the signature), `cmd/bsgo-server` (role `signer`,
included in `all`), `pkg/scheduler/jobstatus` (states), `pkg/storage`
(state CHECK constraint gets `signing`, `finished`; also add `dispatching`
here if not done elsewhere), docs/specs/02.
Tasks: consume job-finished (NATS queue group, per-job idempotency key);
state `signing` while running; steps 1–7 of §3; on success emit job-signed
(indexer consumes it instead of job-finished when the project signs; when the
project has signing disabled the jobstatus handler goes straight to
`finished`); on failure state `failed` with details `signing failed: ...`
(no retry storm: retry with backoff, then fail).
OBS reference: `bs_signer:signjob` (which files are signed, `-T buildtime` for
drpms, delsign/re-sign), `BSSched/BuildJob.pm` job states.
Tests: unit tests with a MemoryStore + fake signd; integration test that signs
a real RPM built in-test and checks `rpm -K` with the exported public key
(skip without rpmbuild/rpmsign); idempotency test (run step twice).

### S4. SLSA provenance signing - DONE

Implemented. The worker already emits an in-toto v1 Statement
(`_type https://in-toto.io/Statement/v1`, subjects = produced RPMs with
sha256, predicate `https://slsa.dev/provenance/v1` with buildDefinition and
runDetails, `pkg/proto/v1/provenance.go`), so S4 only wraps and signs it.

- **Envelope** (`pkg/proto/v1/dsse.go`): `{"payloadType":
  "application/vnd.in-toto+json","payload":base64(statement),"signatures":
  [{"keyid":<fingerprint>,"sig":base64(sig)}]}`. PAE = `DSSEv1 SP len(type) SP
  type SP len(payload) SP payload` (same as `bs_signer:dsse_pae`).
- **Signature format**: raw RSA PKCS#1 v1.5 over SHA-256(PAE) with the project
  RSA key; no OpenPGP framing. Chosen because DSSE verifiers (sigstore dsse,
  slsa-verifier, cosign with an RSA key) expect a bare signature value over the
  PAE, and OBS signs the PAE through `sign -D`, a bare signature as well. The
  obs-sign client source is not available here, so "raw PKCS#1 v1.5 SHA-256" is
  matched to the DSSE spec and to the verifier expectations rather than diffed
  against obs-sign output. Difference to OBS: the envelope carries `keyid`
  (OpenPGP v4 fingerprint of the project key; optional in DSSE, OBS omits it).
- **signd**: new command `RAWSIGN <enckey> <hashalgo> <hexdigest>`
  (docs/specs/05); `signer.Signer.SignRaw` resolves the key (inheritance,
  autoextend) and returns the signature plus fingerprint. The private key never
  leaves signd.
- **Stage** (`pkg/signing/provenance.go`): after the RPMs are signed, verified
  and uploaded, the stage reads `_slsa_provenance.json` of the job, sets the
  subject digests to the sha256 of the signed RPMs (adds missing RPM subjects;
  OBS fixes up subjects the same way, hence provenance is signed last), signs
  and uploads the envelope over the same object key, and returns the updated
  sha256/size in the job-signed answer. The unsigned file is replaced, not
  kept: a job reaches `succeeded` only with a signed envelope. Retries are
  idempotent: a retry reads the envelope of the previous attempt, the payload
  is re-marshaled deterministically and PKCS#1 v1.5 is deterministic, so the
  bytes are identical. A job without a provenance artifact is passed through;
  a job with one needs `Stage.Raw` (wired to the shared signer in
  `cmd/bsgo-server/signing.go`).
- **No key**: same as the RPM stage. `signer.Enabled` is false for a project
  without key when `--sign-create-keys=false`; such jobs skip the stage and the
  provenance stays the worker's unsigned statement. If a job reaches the stage
  anyway, `ResolveKey` fails with `ErrNoKey` before anything is touched.
- **Publisher** (`pkg/publisher/provenance_export.go`): like `bs_publish`, a
  binary `X.rpm` gets `X.slsa_provenance.json` next to it in the repository
  (a copy of the unit's envelope, written before `repomd.xml`). It is exported
  only when the envelope has a signature and its statement lists the binary
  with the published sha256; unsigned (pre-S4) or mismatching provenance is not
  published and a stale copy is removed. Files of unpublished binaries are
  garbage collected with their RPM. The publisher holds no keys and does not
  verify the signature.
- **Verification**: `signing.VerifyProvenance(data, armoredPubKey)` recomputes
  the PAE and checks the RSA signature against the project public key. Tests:
  `pkg/signing/provenance_test.go` (in-process signd engine: verify, tamper,
  wrong key, idempotency, no-key, failure cases),
  `TestStageSignsProvenance` (real rpmbuild/rpmsign and signd over TCP),
  `pkg/signd/rawsign_test.go`, `pkg/proto/v1/dsse_test.go`,
  `pkg/publisher/provenance_export_test.go`.
- **cosign / slsa-verifier** (untested, neither tool is installed here): the
  envelope is a standard DSSE with an RSA PKCS#1 v1.5 SHA-256 signature, so it
  should verify with `cosign verify-blob-attestation --key <project RSA public
  key as PEM> --signature ... --type slsaprovenance1` style tooling after
  converting the key from OpenPGP to PEM (the PEM export is not provided yet;
  the key is the RSA primary key of `repomd.xml.key`). `slsa-verifier` expects
  Sigstore/GitHub-builder identities and does not apply to key-signed
  provenance in this form.

### S5. Key management CLI and ACLs
Files: `pkg/cli` (`bsgo project signkey show|create|extend|export`),
`pkg/auth` (scopes `project:signkey:<project>`).
Tasks: non-interactive, `--json`, `--dry-run`, `--yes` per AGENTS §5; show
fingerprint, algorithm, expiry, inherited-from; export armored public key.
Acceptance: table-driven CLI tests; ACL denies `create/extend` without the
scoped token.

### S6. States in status and watch
Files: `pkg/cli/project_status.go`, watch view model.
Tasks: show `signing` (`S`) and `finished` (`f`) with OBS letters; counts.

## 5. Order and parallelism

S0 → (S1) → S2 ∥ S3 → S4 → S5 ∥ S6. S2 and S3 only need S1's protocol and
the client in `pkg/signer`; land the protocol + client as the first S1 PR.

## 6. SLFO bootstrap note

Re-signing is not needed for binaries already built: once S3 lands, force a
rebuild (or a re-sign of existing build-repo binaries via a one-off
`bsgo admin resign <prpa>` command, optional in S3) before the bootstrap is
declared done. The SUSE:SLFO:1.1 Bootstrap Dependency binaries keep their
SUSE signatures; they are removed with the bootstrap anyway.
