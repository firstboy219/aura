# ADR-0037 — Signed runtime artifacts

Status: **Proposed** (P4-M3, 2026-11-10). Implements Blueprint D.1 P4: "Signed runtime artifact verification is operational for
controlled releases."

## Decision
- A **release manifest** lists the sha256 and size of every runtime file: `/usr/local/bin/aura` and the whole Python
  `site-packages` tree, including bytecode, `.pth` files and `dist-info`. Python would execute any of those. The manifest also
  names the signed directories, which must hold **exactly** the signed files. A planted module, file or `.pyc` fails verification.
- The manifest body is signed with **Ed25519**. The private key (`/opt/aura/release/signing.key`, 0400, Core uid) is outside the
  repository and the image. Only the public key is configured:
  - `release.public_key` in `/opt/aura/config.yaml`, which is outside the image, so a modified image cannot swap the key;
  - `AURA_RELEASE_PUBKEY` (from `deploy/.env`, not committed) for the worker host.
- **Startup:** `aura serve` verifies before opening the database. With `release.required: true` a failure stops startup;
  otherwise it is logged as an error. `aura worker-host` always refuses to serve if verification fails. `aura doctor` reports the
  result.
- **Release procedure:** `deploy/release.sh <version>`:
  1. build the image;
  2. copy the runtime files out of it;
  3. sign them in a throwaway container with no network;
  4. bake `release.json` into the final image;
  5. restart the stack.
- CLI: `aura release keygen | sign | verify`.

## Evidence
- `internal/release` tests: a signed release verifies. A changed binary, an added module, a removed file, an edited manifest and a
  foreign key are each refused.
- Production, 2026-11-10: release `0.2.0-p4` (458 files, key `6be268ad3a932c4b`) verified by the Core and by the worker host.
  Two tampered images, one with an extra `aura_worker/evil.py` and one with a modified `aura` binary, were refused at `aura serve`
  with the exact reason.

## Limits
- The base OS and Python standard library outside `site-packages` are not in the manifest. They come from the pinned base image.
  Signing the whole filesystem is possible later.
- The signing key is on the same server for now. The owner should move it to an offline machine and sign there.
- Verification happens at start, not continuously. The root filesystem is read-only at runtime.
