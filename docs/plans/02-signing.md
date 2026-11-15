# Plan: Signing (RPMs, repository metadata, provenance)

Status: proposed (2026-11-15)
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

## 2. Decisions for the human (resolve before S1)

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

### S0. Remove the fake implementations (small, first)
Delete `SignRPM`/`SignPackageArtifacts` byte-appending code and the PEM
"OpenPGP-looking" detached format; make the publisher skip `repomd.xml.asc`
until S2 lands (no signature is better than an invalid one). Tests updated.
Acceptance: no code path writes non-OpenPGP signatures.

### S1. OpenPGP keys in bsgo-signd (SD1, SD3)
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

### S2. Repository metadata signing (publisher)
Files: `pkg/publisher/subrepo.go`, `pkg/signer/service.go`.
Tasks: `repomd.xml.asc` = armored OpenPGP detached signature (sigtype binary
document, SHA-256) from signd; `repomd.xml.key` and `<repo root>/repodata/...`
plus `<project>.key` at the repository root = armored public key. Upload order
stays: data, then signature + key, then `repomd.xml` last.
Acceptance (integration, podman): `zypper --root /tmp/t ar --gpgcheck-strict
<repo>`, import the key, `zypper ref` succeeds; tampering with `repomd.xml`
makes `zypper ref` fail. Same with `dnf --setopt=gpgcheck=1 makecache` if
available.

### S3. RPM signing stage (SD2)
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

### S4. SLSA provenance signing
Files: `pkg/signing`, `pkg/proto/v1/provenance.go`.
Tasks: wrap the worker's provenance as an in-toto Statement in a DSSE envelope
(`payloadType: application/vnd.in-toto+json`), sign the PAE
(`DSSEv1 <len> <type> <len> <payload>`) with the project key via signd
(raw signature over SHA-256, like `bs_signer:dsse_sign`), store as
`_slsa_provenance.json` (envelope) next to the binaries; publisher exports it.
Acceptance: verification with the project public key in a Go test; optional
`slsa-verifier`/`cosign verify-blob` compatibility documented.

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
