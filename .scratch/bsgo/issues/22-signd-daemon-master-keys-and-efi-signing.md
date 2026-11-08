# 22: Signer: bsgo-signd Bare-Metal Daemon, Database Keys & EFI Signing

**What to build:**
Implement the standalone `bsgo-signd` bare-metal daemon and secure key lifecycle management:
1. **PostgreSQL Key Storage**: Add goose migration `00003_project_signing_keys.sql` and storage methods storing per-project public keys and master-encrypted private keys.
2. **Master Key Engine**: Decrypts project private keys in ephemeral memory with zeroing buffers (`memguard` semantics), supporting both software master keys (AES-256-GCM / RSA-OAEP) and physical Thales Luna HSM PKCS#11 slots.
3. **EFI / UEFI Secure Boot & Kernel PE Signer**: Implements Authenticode PE/COFF image signing for `vmlinuz`, `shim.efi`, and `grub2.efi`, plus CMS/PKCS#7 kernel module signing.
4. **Standalone Daemon (`cmd/bsgo-signd`)**: Listens on TCP / mTLS for signing requests, completely isolated from worker compilation sandboxes.

**Blocked by:** 21: Signer: obs-signd Protocol Client & Hardware Security Module (HSM) Service

**Status:** closed

- [x] Goose migration `00003_project_signing_keys.sql` and PostgreSQL `ProjectKeyStore` interface in `pkg/storage`.
- [x] Master Key decryption engine in `pkg/signd` decrypting project keys in RAM and wiping memory after signing.
- [x] Direct PKCS#11 / Thales Luna HSM hardware signing driver in `pkg/signd`.
- [x] EFI Authenticode PE/COFF image signer and CMS module signer in `pkg/signd/efi`.
- [x] Standalone bare-metal daemon `cmd/bsgo-signd` with mTLS/TCP server.
- [x] Comprehensive unit and integration test suite covering project key generation, encryption, EFI signing, and signature verification.
