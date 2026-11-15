# 05. bsgo-signd protocol and OpenPGP keys

Status: implemented (plan 02, S1). Code: `pkg/signd`, client side `pkg/signer`.

`bsgo-signd` is the only component that ever holds a project private key in
the clear. It speaks a line based text protocol over TCP (optional mTLS, CIDR
allow-list). Signatures are OpenPGP v4 (RFC 4880) so that zypper, dnf, gpg and
rpm accept them.

## Keys

- OpenPGP v4 RSA primary key, default 4096 bits, certify and sign capable, no
  subkeys, SHA-256 preferred hash.
- User ID `<project> OBS Project <project@OBSNAME>`; for a flavor other than
  `default` the flavor is added as the comment: `<project> OBS Project
  (<flavor>) <project@OBSNAME>`. `OBSNAME` defaults to `bsgo`
  (`bsgo-signd --obs-name`).
- Expiry 800 days by default (`--key-days`), like OBS `Signkey.pm`. A key can
  be extended; the fingerprint stays the same.
- At rest: the OpenPGP private entity is serialized, then AES-256-GCM
  encrypted with the master key (`MasterKey`). Same trust model as OBS: the
  `_signkey` is encrypted to the signd master key.
- signd is stateless. The encrypted key (`enckey`, hex) is stored by the
  client in `project_signing_keys` and passed with every request, like
  `sign -P <keyfile>` in obs-signd. This keeps signd free of database
  access and horizontally scalable. The commands therefore take the key blob
  instead of `<project> <flavor>`; project and flavor are only needed to
  generate a key (user ID) and are resolved by `pkg/signer`.
- Signatures are deterministic: go-crypto's random salt notation is disabled.
  The salt would change the hashed trailer between the two protocol phases.

## Why two phases (digest signing)

An OpenPGP v4 signature hashes `document || trailer`, where the trailer is the
signature header and the hashed subpackets (creation time, issuer
fingerprint) followed by `04 ff <length>`. The signer cannot build the trailer
from a finished digest, because it is appended to the hash state. Options:

1. Client sends the whole document to signd. Simple, but large inputs (RPMs,
   ISOs) would cross the network and signd would parse untrusted data.
2. Two phases (chosen). The client asks signd for the trailer, hashes
   `document || trailer` itself and sends only the digest. signd rebuilds the
   identical signature parameters and signs the digest. The private key and
   the signature assembly stay in signd, the document never leaves the
   client. One design serves repomd.xml, provenance, checksums and the RPM
   helper.

Implementation: signd uses go-crypto `packet.Signature.Sign` with a
`hash.Hash` stand-in that records writes (the trailer) and returns the
client's digest from `Sum`. The trailer for TRAILER and SIGN is computed by
the same code from the same parameters, so both calls agree.

Like obs-signd, signd signs the digest it is given and cannot check what it
covers; access control is the CIDR list, mTLS and, later, token ACLs on the
callers. Only signature types 0 (binary document) and 1 (text document) are
accepted, so signd cannot be used to certify other keys. SHA-1 is refused.

## Commands

One command per line, fields separated by single spaces, case-insensitive
command names. Replies: `OK <fields>` or `ERROR: <message>`. Binary values are
hex encoded. A connection may carry several commands; `QUIT` closes it.

| Command | Reply fields |
|---|---|
| `GENKEY <project> [flavor] [days]` | key material |
| `PUBKEY <enckey>` | key material (same `enckey`) |
| `EXTEND <enckey> <days>` | key material with the new expiry |
| `TRAILER <enckey> <hashalgo> <sigtype> <created-unix>` | trailer (hex) |
| `SIGN <enckey> <hashalgo> <hexdigest> <sigtype> <created-unix> <armor\|binary>` | signature (hex) |
| `PING` | `PONG bsgo-signd ready` |
| `QUIT` | `BYE` |

Key material fields: armored public key (hex), `enckey`, fingerprint (upper
case hex), algorithm (`rsa4096`), expiry unix time (`0` none).

`hashalgo` is `sha256`, `sha384` or `sha512`. `created-unix` is the signature
creation time and must be identical in TRAILER and SIGN; use a fixed value for
reproducible signatures (RPM `-T buildtime` in OBS). `SIGN` refuses an expired
key (extend it first). `armor` returns an ASCII armored detached signature
(`-----BEGIN PGP SIGNATURE-----`), `binary` the raw signature packet as used
inside an RPM header.

Client flow for a detached signature:

```
TRAILER enckey sha256 0 1763200000   -> trailer
digest = SHA256(document || trailer)
SIGN enckey sha256 digest 0 1763200000 armor -> signature
```

## Client API (`pkg/signer`)

- `PublicKeyArmored(ctx, project, flavor)` for `repomd.xml.key`.
- `SignDetachedArmored(ctx, data, project, flavor)` for `repomd.xml.asc`
  (binary document, SHA-256, armored).
- `SignStream(ctx, project, flavor, reader, DigestRequest)` hashes a stream
  with the trailer and signs it (binary or armored).
- `Trailer` and `SignDigest` are the low-level calls for `bsgo-sign-helper`.
- `ResolveKey`, `ExtendKey` for key management.

Key inheritance follows `bs_signer:getsignkey`: the project, then its parent
namespaces (`a:b:c`, `a:b`, `a`), the first project with a key of that flavor
wins. A project with no key anywhere gets one created for itself (disable
with `Config.NoAutoCreate`). A key that expires in less than 30 days
(`Config.ExtendBefore`) is extended by `Config.KeyDays` on use, like
`getsignkey autoextend=1`.

## Storage

`project_signing_keys` (migration `00008_signing_keys_openpgp.sql`): the
existing `public_key_armored` and `fingerprint` columns now hold the armored
OpenPGP public key and the v4 fingerprint; `algo` and `expires_at` are new.
Rows written before the migration hold raw RSA keys (`algo = ''`). They are
treated as absent: `pkg/signer` skips them and creates a new OpenPGP key on
first use, which overwrites the row. No signature made with them was ever
published.

## Removed

The commands `SIGN_DIGEST` and `GEN_KEY` (raw RSA PKCS#1 signatures) no longer
exist.
