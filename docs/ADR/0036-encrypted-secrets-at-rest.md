# ADR-0036 — Encrypted secrets at rest, key rotation, backup/restore

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
tested." The P1 limitation in SECURITY_MODEL §4 (file permissions only) is replaced.

## Decision
- Secrets live in the `secrets` table (migration 0005) as AES-256-GCM ciphertext with a random 96-bit nonce. The additional
  authenticated data is the secret's name, so a ciphertext copied to another name does not decrypt. Tampering is detected.
- The 256-bit master key is a base64 file (`secrets.master_key_file`, mode 0400, Core uid). It lives in the secrets mount, not on
  the data volume, so a copy of the database or of a backup does not reveal any secret.
- Any secret setting (`providers.*.key_file`, `tools.web.search.brave_key_file`, `mcp.servers[].key_file`, `webhooks[].secret_file`)
  accepts `vault:<name>` as well as a 0400 file path. A `vault:` reference without a master key stops startup.
- CLI, run where the Core runs, never prints a value: `aura secrets keygen <file>`, `aura secrets import <name> --from-file <p> |
  --stdin`, `aura secrets list` (names and key ids only), `aura secrets rotate`.
- **Rotation:**
  1. Create a new key (`keygen`).
  2. Configure it as `master_key_file`, and the old key as `previous_key_file`.
  3. Run `aura secrets rotate`, which re-encrypts every value under the new key in one transaction.
  4. Remove `previous_key_file`.
  Values under a key that is no longer loaded fail clearly.
- **Backup/restore:** the online SQLite backup (`VACUUM INTO`) carries the ciphertext. A restored database decrypts with the
  master key and not without it.

## Evidence
- `internal/vault` tests: no plaintext in the table or in a backup file; wrong key, tampered value and swapped name are refused;
  rotation moves every value and the old key stops working; rotation without the previous key fails and changes nothing;
  restore works with the key only; key files are 0400 and never overwritten.
- `internal/app` tests: a `vault:` reference resolves at startup, and is refused without a master key.
- Production, 2026-11-10: master key generated in `/opt/aura/secrets` (never displayed). The OpenRouter key was imported and
  `providers.openrouter.key_file` set to `vault:openrouter`. `aura doctor` reports the key decrypting, and a real task completed.

## Limits
The master key sits on the same server as the data. Encryption protects copies of the data volume and of backups, not a fully
compromised host. A KMS/HSM or an owner-held key is a later option. The owner should keep a copy of the master key off the
server: without it, the vault cannot be read. The plaintext `openrouter_key` file is kept until the owner confirms its removal.
