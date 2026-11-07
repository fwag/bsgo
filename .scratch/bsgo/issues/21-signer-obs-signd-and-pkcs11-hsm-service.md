# 21: Signer: obs-signd Protocol Client & Hardware Security Module (HSM) Service

**What to build:**
Implement the `bsgo-signer` subsystem delivering cryptographic package, metadata, and provenance signing. Build a pluggable `SignerDriver` interface supporting:
1. `ObsSigndDriver`: Client protocol connecting to `obs-signd` (TCP port 5167) for Thales Luna HSM, SafeNet, and enterprise GPG appliances.
2. `LocalGPGDriver`: Software-based signing driver for development, CI, and staging environments.
3. RPM header signature injection and detached repository metadata signing (`repomd.xml.asc`, `Release.gpg`, `_slsa_provenance.json.sig`).
4. NATS-mediated signing pipeline (`bsgo.sign.request` -> `bsgo.sign.result`) decoupled from worker sandboxes to safeguard private key material.

**Blocked by:** 20: Supply Chain: SLSA 1.2 Provenance Attestation Pipeline

**Status:** closed

- [x] Pluggable `SignerDriver` interface in `pkg/signer/driver.go`.
- [x] `ObsSigndDriver` client implementation connecting to `obs-signd` daemon (TCP 5167) with project/flavor scoping and Thales Luna HSM compatibility.
- [x] Detached signature generator and RPM header signature injector.
- [x] NATS-mediated `SignerService` ingesting sign requests and emitting signed results.
- [x] Integration with `bsgo-publisher` ensuring all published repositories contain cryptographic signatures and public keyrings.
- [x] Comprehensive unit and integration test suite verifying signing protocol, key retrieval, signature validation, and tamper detection.
