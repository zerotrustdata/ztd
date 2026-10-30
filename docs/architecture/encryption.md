---
doc_id: encryption
title: Encryption Architecture
audience: maintainers and coding agents
status: current
version: v1.5.0
last_updated: 2026-10-29
owners:
  - internal/services/vault/
  - internal/services/keystore/
  - internal/services/storage/
  - internal/services/scrubbing/
  - internal/services/gateway/gateway_certs.go
  - internal/services/gateway/secret_manager.go
  - internal/cli/cmd/vault/
  - internal/cli/auth/identity_keys.go
related:
  - docs/architecture/auth.md
  - docs/architecture/governance.md
  - docs/architecture/network.md
  - docs/architecture/storage.md
  - docs/devs/troubleshooting.md
  - docs/reference/fips140-3.md
when_to_read: Implementing, operating, or auditing encryption at rest, vault lifecycle, platform keystore secret isolation, payload scrubbing, or TLS transport security.
do_not_use_for:
  - Network PKI hierarchy, trust bundles, and revocation (docs/architecture/network.md)
  - Detailed storage schema and persistence boundaries (docs/architecture/storage.md)
  - Principal identity binding and authentication routes (docs/architecture/auth.md)
  - FIPS 140-3 module compliance details (docs/reference/fips140-3.md)
---

# Encryption Architecture

## Purpose

Defines the cryptographic boundaries, key hierarchies, storage controls, and runtime lifecycle for data at rest, platform secrets, scrubbing tokens, and transport security within ztd. Specifies how the encryption vault, platform keystore, sovereign scrubbing boundary, and TLS mechanisms enforce security posture across Gateway and Operator runtimes.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Vault Encryption Architecture](#vault-encryption-architecture)
- [Platform Keystore Architecture](#platform-keystore-architecture)
- [Scrubbing and Execution-Site Rehydration](#scrubbing-and-execution-site-rehydration)
- [TLS and Certificate Protection](#tls-and-certificate-protection)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

Invariant groups: [Vault encryption](#vault-encryption-inv-enc-vault), [Platform keystore](#platform-keystore-inv-enc-store), [Scrubbing and rehydration](#scrubbing-and-rehydration-inv-enc-scrub), [Transport security](#transport-security-inv-enc-tls).

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Vault encryption (`INV-ENC-VAULT`)

| ID | Rule |
| --- | --- |
| INV-ENC-VAULT-01 | Every runtime tree MUST maintain an independent vault. A 32-byte vault private key MUST derive a Key Encryption Key (KEK) using HKDF-SHA256 with info `"ztd-lfaa-kek-v1"`. The KEK MUST wrap a randomly generated 32-byte Data Encryption Key (DEK) using AES Key Wrap (RFC 3394). |
| INV-ENC-VAULT-02 | The unwrapped DEK MUST exist only in process memory while the vault is unlocked. Calls to `Lock()` or `Close()` MUST zero the in-memory DEK via `SecureZero()`. |
| INV-ENC-VAULT-03 | All vault-encrypted records MUST use AES-256-GCM with a fresh 12-byte random nonce prepended to the ciphertext (`NonceSize + len(ciphertext)`). |
| INV-ENC-VAULT-04 | Vault header verification MUST match the derived private key fingerprint (`SHA-256("ztd-vault-fingerprint-v1" + privateKey)[:16]`) against the header JSON before DEK unwrapping is attempted. |

### Platform keystore (`INV-ENC-STORE`)

| ID | Rule |
| --- | --- |
| INV-ENC-STORE-01 | Platform secrets MUST be encrypted at rest with a dedicated 32-byte master key stored in an OS-native keyring (freedesktop Secret Service or an ssh-agent seal on Linux, Keychain on macOS, DPAPI on Windows). Key selection MUST fail closed: no backend MUST write the master key in plaintext, and a failing backend MUST NOT fall through to another source. The runtime tree MUST NOT hold the master key in plaintext: the only master-key seal files under `.ztd/secrets/` are the Windows DPAPI seal (`.master_key.dpapi`) and the Linux ssh-agent seal (`.master_key.ssh-agent`). |
| INV-ENC-STORE-02 | Secrets stored under `.ztd/secrets/` MUST use AES-256-GCM authenticated encryption and JSON metadata specifying format version `2`, nonce, and ciphertext. The additional data MUST bind each secret file's ciphertext to its name, and each sealed value to the binding its caller supplies (for example the public-feed path it is stored at), so ciphertext moved to another name or binding fails authentication. Other format versions MUST be rejected. There is no migration from version `1`. |
| INV-ENC-STORE-03 | Keystore initialization MUST enforce strict private filesystem permissions (`0700` for `.ztd/secrets/` and `0600` for secret files). Startup MUST fail if master key generation or permissions enforcement fails. |
| INV-ENC-STORE-04 | A key store with no master key MUST NOT generate one while `.ztd/secrets/` holds secrets sealed by the keystore; startup MUST fail with `ErrKeyStoreOrphanedSecrets` instead of orphaning them under a new key. |
| INV-ENC-STORE-05 | Secret names MUST be a single non-hidden path component. Hidden names are reserved for sealed master keys and in-flight atomic writes, so no secret operation MUST overwrite, read, or delete them or leave the secrets directory. The one exception is `Keystore.Purge()`, which removes every non-directory entry under `.ztd/secrets/`, hidden names included. |
| INV-ENC-STORE-06 | Client identity secrets outside `.ztd/secrets/` MUST be sealed with the same keystore and master key: the CLI private key (`.ztd/cli.key`), each app private key (`.ztd/apps/<name>.key`), and the pending app enrollment state (`.ztd/pki/pending-enrollment/app_<name>.json`, key plus token). Each MUST be stored as a sealed value bound to its runtime-relative path (`Keystore.Encrypt`), read by unsealing in memory then `tls.X509KeyPair`, and sealed under the platform key store master key (OS keyring or ssh-agent). A missing or locked master key MUST fail closed; a plaintext file at a sealed path MUST fail with `ErrIdentityFilePlaintext`, and unseal failures MUST wrap `ErrIdentityFileUnseal`. There is no migration. External clients MUST NOT read these keys: they use the `ztd mcp stdio` bridge, which unseals in process. |
| INV-ENC-STORE-07 | The first time `gw start`, `gw restart`, or `operator start` creates a master key, the invoking terminal MUST show that key once as a recovery key and MUST NOT store it until the operator retypes it. A start with no terminal MUST fail with `ErrKeyStoreRecoveryRequired` unless `--accept-unrecoverable` is set. The recovery key MUST NOT be written into the runtime tree, a start log, or an error string. A declined or mismatched confirmation MUST leave the key store unchanged. `gw recover` and `operator recover` MUST install a saved key only when the OS key store is empty, already holds that same key, or an ssh-agent seal cannot be read because its sealing key is not loaded (`ErrKeyStoreSSHAgentKeyMissing`). They MUST refuse to replace a different readable key. When sealed secrets exist, the key MUST decrypt them before it is stored. A missing ssh-agent sealing key on `gw start` MUST name `gw recover`. |

### Scrubbing and rehydration (`INV-ENC-SCRUB`)

| ID | Rule |
| --- | --- |
| INV-ENC-SCRUB-01 | The Sovereign Execution Boundary MUST scrub outgoing payloads with strict mode enabled by default (`StrictMode: true`), replacing each sensitive value with a persisted, reversible `{{UEI_<owner>_N}}` token while keys and field names stay visible. It MUST NOT emit fixed labels or blank a field, and MUST fail closed to `{{UEI_UNAVAILABLE}}` when the token cannot be persisted. |
| INV-ENC-SCRUB-02 | Reversible User-Explicit Information (UEI) placeholders MUST follow the `{{UEI_<owner>_N}}` pattern, where `<owner>` is the minting runtime's 12-hex-character token owner, persisted without expiry under `constants.ScrubbingTokenOwnerKey` (`uei_owner`). A runtime whose owner cannot be loaded or minted MUST NOT construct its scrubber (`ErrScrubbingTokenOwner`). Mapped values MUST be encrypted via the vault into the observed KV store with a 24-hour TTL under the full key `constants.SentinelKeyPrefix` (`ztd:sentinel:`) + `constants.ScrubbingTokenKeyPrefix` (`uei_token_`) + the token, for example `ztd:sentinel:uei_token_{{UEI_3f9a0c12be47_1}}`. L5 Actuator MUST rehydrate placeholders in the string fields and string map keys of the action's decoded protobuf message immediately before action execution, never in the payload's wire bytes, and MUST resolve only tokens its own runtime minted: token mappings never cross runtimes. A token minted by another runtime, or one with no owner, MUST NOT resolve locally. In an `INFERENCE` payload it passes through opaque, so the model sees the placeholder. In every other action it fails the payload. A payload with an own placeholder in a value or an object key that cannot be resolved, a foreign or ownerless placeholder in a non-`INFERENCE` action, or two object keys that rehydrate to the same name, MUST NOT be dispatched: the handler MUST NOT run, and the receipt MUST finalize FAILED with `failure_code` `RECEIPT_FAILURE_CODE_REHYDRATION_FAILED` (signed, from `ErrL5ActuatorRehydrate`, which wraps `ErrScrubbingTokenUnresolved`, `ErrScrubbingKeyCollision`, or a payload that does not decode as its action's message). |
| INV-ENC-SCRUB-03 | L5 Actuator execution MUST fail closed with `ErrEgressScrubberUnavailable` if an active scrubber is unavailable when scrubbing the target resource on receipt generation. |

### Transport security (`INV-ENC-TLS`)

| ID | Rule |
| --- | --- |
| INV-ENC-TLS-01 | The Gateway HTTPS listener MUST enforce TLS 1.3. Workload and Operator ingress routes MUST require mTLS with client certificates bound to SPIFFE URIs under the `ztd.local` trust domain. |
| INV-ENC-TLS-02 | CA private keys (Root CA, Hub CA, Operator CA, Gateway Peer CA) and Gateway serving certificate private keys MUST be encrypted in the platform keystore. Public certificates and trust bundles persist in `.ztd/pki/`. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Vault core & AES-GCM/KW crypto | `internal/services/vault/` | `./ztd test unit --pkg ./internal/services/vault` |
| Platform Keystore & Keyrings | `internal/services/keystore/` | `./ztd test unit --pkg ./internal/services/keystore` |
| Secret Manager | `internal/services/gateway/secret_manager.go` | `./ztd test unit --pkg ./internal/services/gateway --run TestSecretManager` |
| Execution Record (`events`) | `internal/services/storage/local_first_events_store.go` | `./ztd test unit --pkg ./internal/services/storage` |
| Sovereign Execution Scrubber | `internal/services/scrubbing/` | `./ztd test unit --pkg ./internal/services/scrubbing` |
| Encrypted KV Adapter (UEI) | `internal/services/gateway/encrypted_kv_adapter.go` | `./ztd test unit --pkg ./internal/services/gateway --run TestEncryptedKVAdapter` |
| PKI Authority & TLS | `internal/services/gateway/gateway_certs.go` | `./ztd test unit --pkg ./internal/services/gateway --run TestPKIAuthority` |
| Vault CLI Commands | `internal/cli/cmd/vault/` | `./ztd vault status` |
| Sealed client identity keys | `internal/cli/auth/identity_keys.go` | `./ztd test unit --pkg ./internal/cli/auth --run TestIdentityKey` |

## Vault Encryption Architecture

### Key Hierarchy and Envelope Encryption

ztd uses envelope encryption for vault-protected data at rest. Each runtime environment initializes an independent vault hierarchy:

```text
+-------------------------------------------------------------------+
|                        Platform Keystore                          |
|         Master Key (OS Keyring / ssh-agent / DPAPI)               |
+-------------------------------------------------------------------+
                                  │
                  AES-256-GCM seals .ztd/secrets/vault_key
                                  ▼
+-------------------------------------------------------------------+
|                    Vault Key (32 bytes random)                    |
+-------------------------------------------------------------------+
                                  │
                 HKDF-SHA256 (info: "ztd-lfaa-kek-v1")
                                  ▼
+-------------------------------------------------------------------+
|                   Key Encryption Key (KEK, 32B)                   |
+-------------------------------------------------------------------+
                                  │
                 AES Key Wrap (RFC 3394 / aes-256-kw)
                                  ▼
+-------------------------------------------------------------------+
|                 Wrapped DEK (in .ztd/vault/vault.header)          |
+-------------------------------------------------------------------+
                                  │
                              Unwraps to
                                  ▼
+-------------------------------------------------------------------+
|             Data Encryption Key (DEK, 32 bytes in memory)        |
|             AES-256-GCM + 12-byte random nonce per payload        |
|             Zeroed on Lock() or Close() via vault.SecureZero()    |
+-------------------------------------------------------------------+
```

1. **Vault Key**: A randomly generated 32-byte (256-bit) private key sealed under the master key in the platform keystore as `vault_key` (persisted under `.ztd/secrets/vault_key` using AES-256-GCM).
2. **Key Encryption Key (KEK)**: Derived from the vault key using HKDF-SHA256 with salt `nil` and info string `"ztd-lfaa-kek-v1"`.
3. **Data Encryption Key (DEK)**: A randomly generated 32-byte AES-256 key (`crypto/rand`).
4. **Key Wrapping**: The DEK is wrapped by the KEK using AES Key Wrap (RFC 3394 / `aes-256-kw`).
5. **Vault Header**: Persisted to `.ztd/vault/vault.header` as JSON containing format version `1`, creation timestamp, KDF parameters, KEK algorithm identifier, base64-encoded wrapped DEK, and hex-encoded private key fingerprint (`KeyFingerprintSize = 16` bytes of `SHA-256("ztd-vault-fingerprint-v1" + vaultKey)`).
6. **Rekey Flow**: Rekeying generates a fresh vault key and rewraps the existing DEK under a new KEK, staging the new key at `.ztd/secrets/vault_key.rekey` and updating the header before sealing the new key again under `vault_key` (sealed secrets are bound to their names, so the staged file cannot be renamed into place) and removing the staged copy. If that final write fails, the staged key still opens the vault and the error names it.

When unlocked, the unwrapped DEK remains strictly in process memory (`Vault.dek`). Calls to `Vault.Lock()` or `Vault.Close()` clear the DEK using `vault.SecureZero()`.

### Data Encryption and Protected Surfaces

Data fields are encrypted using AES-256-GCM (`crypto/cipher.NewGCM`) with a fresh 12-byte random nonce generated per payload (`GenerateNonce()`). The 12-byte nonce is prepended directly to the GCM ciphertext before persistence. Stores encrypt content only through `Vault.EncryptWithAAD`/`Vault.DecryptWithAAD`, which bind associated data that names the store and, where the location is stable, the record, so a ciphertext copied into another store or record fails authentication.

Vault encryption protects high-sensitivity fields across platform services:

- **Execution record** (`events`, `internal/services/storage/local_first_events_store.go`): `command_stdout` and `command_stderr` hold the full raw output, gzip-compressed and then encrypted with the vault. `command_raw` and `content_text` are encrypted. Structured metadata (exit code, duration, `execution_id`, `operator_session_id`, hashes) stays plaintext for indexing. Per-file diff statistics are stored encrypted in `file_mutation_log.diff_stat` (see the list below).
- **Local-First Events Store** (`internal/services/storage/local_first_events_store.go`, `commitment_ledger.go`): Encrypted content columns of `ztd.db`. Every write encrypts through the runtime vault and fails with `ErrLocalFirstEventsStoreVaultLocked` when the vault is locked or absent. There is no plaintext branch and no "encrypted" flag column. Reads decrypt on the owning runtime; a locked vault fails the read. Each ciphertext is bound to its column and row identity as AES-GCM associated data (`Vault.EncryptWithAAD`), and keyed digests use `Vault.HMACSHA256`, which derives a purpose-bound subkey from the DEK with HKDF-SHA256 so the DEK itself is never a MAC key.
  - `events`: `content_text`, `command_raw`, `command_stdout`, `command_stderr`, `event_receipt_target_resource`, `event_receipt_result_summary` (BLOB). The canonical protojson EventReceipt is `content_text` on the `ztd.v1.operator.receipt.recorded` event. The signature stays plaintext and is verified over the stable canonical signing payload, not over these columns.
  - `commitment_ledger`: `target_resource`, `attestation_json` (BLOB). Commitment hash and signature checks run over decrypted values.
  - `file_mutation_log`: `filepath`, `diff_stat` (BLOB).
  - `sessions`: `title`, `user_identity` (BLOB).
  - `suspended_transactions`: `envelope`, `tool_arguments` (BLOB).
  - Plaintext by design: identifiers, hashes, signatures, `seq`, `prev_hash`, `content_digest` (an HMAC-SHA256 under a vault-derived subkey, not a hash of the content), `hash`, `transaction_id`, timestamps, event types, statuses, exit codes, durations, sizes, and truncation flags. Audit-chain checkpoint payloads (`events.content_text` rows of type `ztd.v1.platform.audit.chain.checkpointed`) are encrypted like other `content_text` values.
  - Exports carry metadata, hashes, and signatures only. No compliance, operational, or `ztd report` export writes decrypted content. Chain-segment verification uses the stored `content_digest` and hash linkage; an exported receipt-recorded entry also carries the plain SHA-256 of the exported receipt body (`content_sha256`), recorded by the exporting runtime after it verified the keyed digest, because an offline importer cannot recompute the keyed digest. Other chained entries in the segment carry no plain content hash, since their content is never exported.
- **UEI Token Store** (`internal/services/gateway/encrypted_kv_adapter.go`): Reversible User-Explicit Information (UEI) token values are encrypted via `EncryptedKVAdapter` using the vault DEK before storage in `KVStoreService`. The adapter prefixes every key with `constants.SentinelKeyPrefix` (`ztd:sentinel:`), and the scrubber stores each token under `constants.ScrubbingTokenKeyPrefix` (`uei_token_`) after that prefix. Each value is bound to its full KV key as associated data. Keys and TTL metadata remain visible.
- **File Ledger** (`internal/services/storage/ledger.go`): Every file snapshot is encrypted and stored with an `.enc` suffix, bound to its git-relative snapshot path (`files/<keyed name>.enc`) as associated data. Mirroring fails closed with `ErrLedgerVaultRequired` when the vault is locked, and no plaintext copy is written. Ledger diffs are computed over decrypted snapshots in memory.

### Gateway Startup and Vault Lifecycle

Gateway startup reads `--vault-dir` (default: `.ztd/vault`). Headless or container hosts without a usable OS keyring use the ssh-agent seal described under [Master Key and Keyring Providers](#master-key-and-keyring-providers).

If no vault header exists on startup, Gateway automatically initializes a new vault header and generates the sealed vault key in the platform keystore. If an existing key cannot be read, decrypted, or matched to the header fingerprint, Gateway startup fails closed. When locked, audit, execution-record, suspended-transaction, file-ledger, and UEI writes fail closed, and reads that need decrypted content fail with `ErrLocalFirstEventsStoreVaultLocked`. The runtime never returns ciphertext in place of content. Restore access to the original OS keyring or SSH sealing key to reopen retained state. `ztd gw clean` archives the runtime and starts a new trust domain; it does not recover encrypted data.

## Platform Keystore Architecture

### Master Key and Keyring Providers

The platform keystore (`internal/services/keystore/keystore.go`) protects long-lived operational security material. It uses a dedicated 32-byte master key independent of the vault DEK.

The master key is stored using OS-native keyrings, failing closed if no protected store is available. Secret Service and Keychain hold the key outside the runtime tree. DPAPI and the ssh-agent seal write a sealed blob to a hidden file in `.ztd/secrets/` (`constants.MasterKeyDPAPIFilename`, `constants.MasterKeySSHAgentFilename`); the blob is ciphertext that only the sealing user (DPAPI) or the holder of the sealing SSH private key (ssh-agent) can open, so a copied runtime tree or backup does not expose the master key. No backend writes the master key in plaintext. A `Keystore` reads the master key from its store once and holds it in process memory, like the vault DEK, until `Close()` (called by `CanonicalDBService.Close()`) or `Purge()` zeroes it with `vault.SecureZero()` (`Purge()` also deletes the master key from its store and removes every non-directory entry under `.ztd/secrets/`, hidden seal files included); encrypting or decrypting a secret never reads the key store again, so a store that asks the user for access (the macOS Keychain) asks at most once per process.

- **Linux**: Uses the freedesktop Secret Service API (`newSecretServiceKeyring()`), spoken directly over the session D-Bus (e.g. `gnome-keyring-daemon`, KWallet); no `secret-tool` or libsecret install is required. ztd never autolaunches a session bus. Construction checks that an existing master key item is readable or, when none exists, that the `default` collection alias resolves; a host with only the in-memory `session` collection (autologin or SSH-only sessions whose login keyring was never created) never stores a key there, because it would be lost at logout; such hosts use the ssh-agent seal below. Locked items and collections are unlocked through the service prompt. New keys are created without replacing an existing item, and more than one matching item fails closed. Desktop sessions use this backend.
- **Linux ssh-agent seal**: Used when `.ztd/secrets/.master_key.ssh-agent` already exists, or when the Secret Service is unreachable or has no `default` collection. ztd uses the agent named by `SSH_AUTH_SOCK`; when it is unset (SSH logins without `-A`), ztd dials the user's systemd agent sockets under `XDG_RUNTIME_DIR` (default `/run/user/<uid>`) in order: `gcr/ssh` (`gcr-ssh-agent.socket`, GNOME and Ubuntu desktops) then `ssh-agent.socket`. The agent signs `ztd-master-key-seal-v1:<salt>` with its first Ed25519 key (else RSA, using `rsa-sha2-512`). Those signatures are deterministic, so HKDF-SHA256 over the signature re-derives the same key-encryption key. The master key is sealed with AES-256-GCM, with the SSH public key as additional data, and the sealed blob holds no plaintext key. Unsealing requires the same SSH key in a reachable agent, either local or forwarded with `ssh -A`. The agent socket must be served by the service user or root (checked with `SO_PEERCRED`), because whoever serves it chooses the sealing key and sees every signature. ECDSA and security-key (`sk-`) keys are not used. Other Secret Service failures fail closed and never fall through to ssh-agent. Containers and headless hosts without a user agent run an ssh-agent with a loaded key (`eval "$(ssh-agent)" && ssh-add`, after `ssh-keygen -t ed25519` if the host has no key). Without an agent key, ztd fails closed.
- **macOS**: Uses the user's default Keychain (`newKeychainKeyring()`) through Security.framework, called without cgo. The master key is a generic password item (service `ztd-platform`, account `master-encryption-key`) created by the ztd process itself, so its access list trusts only that ztd binary; other programs, including the `security` command, must pass a Keychain prompt to read it. Because macOS identifies the binary by its code signature (an unsigned binary by its exact bytes), the first run of a rebuilt or upgraded unsigned ztd asks once for access, and the read blocks until the user answers; without a GUI session that prompt cannot be shown and startup fails with `ErrKeyStoreLocked`. A read that blocks longer than `constants.KeyStoreApprovalNoticeDelay` logs `constants.KeyStoreAwaitingApprovalLog`; a background `gw start` that finds it in the child's log tells the user to approve the prompt and waits up to `constants.KeyStoreApprovalTimeout`. New keys never replace an existing item. Items created by earlier releases through the `security` command are not read.
- **Windows**: Uses Windows DPAPI (`newDPAPIKeyring()`) through `CryptProtectData`/`CryptUnprotectData` in `golang.org/x/sys/windows`, scoped to the current user with entropy `ztd-platform/master-encryption-key`. The sealed blob is stored at `.ztd/secrets/.master_key.dpapi`; only the same user on the same machine can unseal it.

The current code has no external file backend or `--master-key-file` flag, and the launch profile records no key source. A launch profile that still contains `master_key_file` fails closed with `ErrLaunchProfileCorrupted` (the decoder disallows unknown fields); see [Upgrading a v1.0.x Gateway](#upgrading-a-v10x-gateway). Outbound Operators select their key source on their own host.

On Linux, `gw start` checks an existing ssh-agent seal before network identity detection and process launch; `gw restart` checks it before stopping the current process. This check only reads and unseals the existing master key, then zeroes it. A fresh runtime continues through normal provider selection. A missing or unreachable agent returns a typed `SSHAgentUnlockError` carrying the required public-key fingerprint. The CLI searches regular `~/.ssh/*.pub` sidecars for that fingerprint and a corresponding private-key file, then prints shell-quoted copy-and-paste commands and a retry command preserving explicit flags. With terminal input and a reachable agent named by `SSH_AUTH_SOCK`, it asks before invoking `ssh-add -t 8h` for that matching path. OpenSSH owns the passphrase prompt; ztd does not read the private key or passphrase. Startup continues only after the original master key decrypts successfully. Nonterminal input, refusal, unavailable agents, and unmatched keys fail closed with recovery instructions, including `gw recover` when the sealing key is gone. No agent is started automatically, no shell profile is changed, and loading the original SSH key never replaces the seal or changes providers. `gw recover` re-seals under the key the agent holds now only after the saved recovery key decrypts the existing secrets. The eight-hour limit applies to the newly loaded agent key; a running Gateway retains its unlocked master key until it stops.

OS key creation occurs only during initialization when lookup reports a missing key. Gateway and Operator start show the recovery key and store it only after the operator retypes it (`INV-ENC-STORE-07`). The new key is then read back and compared in constant time; store, retrieval, and verification failures abort startup without switching backends or deleting a stored key. Backend construction performs no write probe.

The CLI recovery-key ceremony can run before the runtime tree exists. It initializes the master key without enforcing secrets-directory permissions. Gateway and Operator service startup create the runtime tree before opening the keystore and enforcing those permissions.

Changing to a different valid key is not migration or rotation: existing ciphertext will not decrypt. Restore the original key and configuration to recover. Never overwrite an existing key to repair startup; loss of the master key loses access to its encrypted data.

### Recovery key

The recovery key is the master key, encoded for a person to write down. It is not a second key and it is not wrapped. `gw start` and `operator start` generate it in memory, print it on the terminal once, and store it in the OS key store only after the operator types it back. A mismatch, an empty answer, or a closed terminal stores nothing. The next start generates a different key; the one that was not confirmed is void.

The key is not written under `.ztd/`, not logged, and not put in an error. Terminal scrollback holds a copy until the operator clears it. Anyone who has the key can open that runtime. Losing both the paper copy and the OS key store means that gateway or operator cannot be restored. There is no backdoor and `gw clean` does not bring it back.

On macOS Keychain and Linux Secret Service, one login holds one master key and every runtime on that login uses it, so the ceremony runs on the first create only. On the Linux ssh-agent seal and Windows DPAPI, each runtime tree has its own sealed master key, so each new gateway or operator runtime runs the ceremony.

`--accept-unrecoverable` stores the key without showing it. Automated tests, scale runs, and the cloud systemd unit pass it because they have no terminal. Losing that OS key store then destroys the runtime. `gw recover` and `operator recover` read a saved key from the terminal and install it. They do not replace a different key the store can still read. An ssh-agent seal whose sealing key is not loaded is treated as empty: the saved key must decrypt every sealed secret, and the seal is rewritten under the key the agent holds now. When `.ztd/secrets/` already holds sealed secrets, the key must decrypt them or nothing is stored.

A local `operator deploy` runs the ceremony in the deploying terminal before it starts workers, so the key is not written to the worker start log. A remote deploy has no terminal on the far side: the worker refuses to create a key unless the deploy passes `--accept-unrecoverable`, or an operator runs `operator start` in a terminal on that host first.

### Secret Storage Format

Secrets managed by `SecretManager` (`internal/services/gateway/secret_manager.go`) are written to individual JSON files under `.ztd/secrets/<name>` using AES-256-GCM. The JSON format includes `version: 2`, base64 `nonce`, and base64 `ciphertext`, and the ciphertext is bound to the file name (`INV-ENC-STORE-02`).

The platform keystore protects:

- Vault private key (`vault_key`, `vault_key.rekey`)
- Actuator signing key (`actuator_signing_key`) and Key ID (`actuator_key_id`)
- Auditor signing key (`auditor_signing_key`), Key ID (`auditor_key_id`), and HMAC key (`auditor_hmac_key`)
- Root CA, Hub CA, Operator CA, and Gateway Peer CA private keys (`ca_<type>_key`)
- Gateway serving certificate private key

The CLI private key is not in this list: it is sealed at `.ztd/cli.key` (INV-ENC-STORE-06).

Public-feed secrets are not files under `.ztd/secrets/`. `WritePublicSecret` seals each one with `Keystore.Encrypt` bound to its runtime-relative path: the signing key (`public-feed/signing-key.ed25519`) and ingest token (`public-feed/ingest-token`). A pending signing-key rotation stores its sealed new private key as `sealed_new_private_key` in `public-feed/key-rotation.json`.

Startup enforces directory permissions (`0700`) and secret file permissions (`0600`) via `Keystore.EnforcePermissions()`.

## Scrubbing and Execution-Site Rehydration

ztd combines tokenizing output scrubbing with reversible execution-site rehydration across the sovereign execution boundary:

```text
+---------------------------------------------------------------------------------+
|                                Ingress / Model Run                              |
|      Sensitive input registered -> Reversible placeholder {{UEI_<owner>_1}}     |
|           Plaintext mapped to Vault-encrypted KV under "uei_token_" (24h TTL)   |
+---------------------------------------------------------------------------------+
                                         │
                                         ▼
+---------------------------------------------------------------------------------+
|                       Governance Pipeline (L1–L4 Verification)                  |
|            Payload carries only {{UEI_<owner>_1}} placeholder token             |
|             L1 Doctrine, L2 Consensus, L3 Notary inspect scrubbed payload       |
+---------------------------------------------------------------------------------+
                                         │
                                         ▼
+---------------------------------------------------------------------------------+
|                          L5 Actuator Execution Boundary                         |
|     1. rehydratePayload: Replaces own tokens in values and keys with plaintext  |
|        via ScrubbingService.RehydratePayload(). Fail-closed: an unresolvable    |
|        or (outside INFERENCE) foreign placeholder never dispatches: handler     |
|        skipped, receipt FAILED. INFERENCE keeps foreign tokens opaque           |
|     2. Capability minting and command execution with rehydrated parameters      |
|     3. Outbound receipt generation: Scrubber active check enforced;             |
|        scrubs target_resource and result_summary before signing EventReceipt   |
+---------------------------------------------------------------------------------+
```

1. **Tokenizing Scrubbing**: The Sovereign Execution Boundary (`internal/services/scrubbing/boundary.go`) inspects outgoing model payloads, command output, and event streams. Each detected credential, API key, private key, connection string, email address, or personal-data value (card, SSN, phone, IBAN) is replaced with its own persisted `{{UEI_<owner>_N}}` token. A 16-digit card number is tokenized only when it passes the Luhn checksum, because the shape alone also matches random identifiers such as an all-digit hex workspace ID — never a fixed label and never an empty field. `<owner>` is the runtime's token owner: 6 random bytes, hex-encoded, minted by the first scrubber on the runtime's store and persisted without expiry under `constants.ScrubbingTokenOwnerKey`. The Gateway and its embedded Operator share one owner; each outbound Operator has its own. Only the sensitive value is tokenized: keys, field names, and surrounding operational text stay visible (`password={{UEI_3f9a0c12be47_3}}`, `{"password":"{{UEI_3f9a0c12be47_3}}","user":"svc-deploy"}`). Strict mode (`StrictMode: true`, enabled by default) adds a key/value pass over sensitive key names. Existing tokens are never re-tokenized. If the token cannot be persisted, the value becomes `{{UEI_UNAVAILABLE}}` (fail closed, no plaintext). Constructing an enabled scrubber that requires persistence without a token store fails with `ErrScrubbingTokenStoreUnavailable`.
2. **Reversible UEI Rehydration**: Callers register sensitive input strings to obtain a `{{UEI_<owner>_N}}` token. The value is encrypted into the observed KV store via `EncryptedKVAdapter` with a 24-hour expiration under the full key `constants.SentinelKeyPrefix` (`ztd:sentinel:`) + `constants.ScrubbingTokenKeyPrefix` (`uei_token_`) + the token. Immediately prior to action execution, the L5 Actuator (`internal/services/governance/l5_actuator.go`) rehydrates placeholders with original un-scrubbed values via `rehydratePayload`. Only tokens that name the executing runtime's owner resolve; plaintext mappings never cross runtimes, so a token minted elsewhere can never select a local secret. A foreign or ownerless token (for example a Data Operator's token in a tool result carried into the next turn on a dedicated Inference Operator) is governed by a typed `scrubbing.ForeignTokenPolicy`: `INFERENCE` uses `ForeignTokenOpaque`, which leaves the placeholder in the prompt so no other runtime's secret reaches a model provider; every other action uses `ForeignTokenReject`, which fails the payload with `ErrScrubbingTokenUnresolved`. Rehydration is fail-closed. If `RehydratePayload` returns an error, the actuator does not dispatch: the execution handler never runs, and the receipt finalizes FAILED with the rehydration error as its cause (`ErrL5ActuatorRehydrate`). `RehydrateText` resolves a token from the in-memory map, then from the token store. A placeholder that resolves from neither, or whose store lookup fails (for example a locked vault), returns an error wrapping `ErrScrubbingTokenUnresolved`; the text is never returned with the placeholder left in it, and a payload with one unresolved placeholder is never partially rehydrated. Tokens expire from the store after 24 hours, so after a restart a placeholder minted longer ago fails closed rather than reaching the handler. Object keys are rehydrated through the same resolver as values, because `ScrubText` scans the whole payload text and can tokenize an email-shaped key (for example `{"bob@x.io":1}` becomes `{"{{UEI_3f9a0c12be47_1}}":1}`). An unresolved key fails the whole payload. Two distinct keys that rehydrate to the same name fail the whole payload with `ErrScrubbingKeyCollision`, because keeping one would silently overwrite the other.
3. **Outbound Receipt Scrubbing**: Before signing, L5 Actuator scrubs `target_resource` on a copy of the envelope and scrubs result summaries and failure causes. Without an active scrubber, the target resource fails closed with `ErrEgressScrubberUnavailable`, and result text is replaced with the typed status.

## TLS and Certificate Protection

Network transport encryption relies on TLS 1.3 and SPIFFE identity binding managed by `PKIAuthority` (`internal/services/gateway/gateway_certs.go`):

- **CA Hierarchy**: ECDSA P-256 Root CA (`pki/root/root_ca.crt`), separate Hub CA, Operator CA, and Gateway Peer CA (`pki/authorities/`). CA private keys are stored exclusively in the platform keystore.
- **Serving Certificate**: Gateway issues a 90-day ECDSA P-256 serving certificate (`pki/issued/hub/operator-gateway.crt`, chain in `pki/issued/hub/operator-gateway.chain.pem`) with DNS SANs including `localhost`, `ztd.local`, and configured IPs. Serving private keys are stored in the platform keystore.
- **Mutual TLS (mTLS)**: Workload and Operator connections on port 8443 enforce TLS 1.3 mTLS and validate SPIFFE URI SANs (`spiffe://ztd.local/...`) against server-side OperatorSession and CLISession records.
- **Plain HTTP Surface**: Port 8080 serves a restricted plain-HTTP bootstrap and discovery surface.

See [Network Architecture](network.md) for PKI hierarchy details and [Authentication & Authorization Architecture](auth.md) for route authentication modes.

## Procedures

### Initializing the Vault

Generate a new vault header and seal a new vault key in the platform keystore. On Linux without a Secret Service keyring, load an ssh-agent key first (`eval "$(ssh-agent)" && ssh-add`).

```bash
./ztd vault init
```

### Unlocking and Validating the Vault

Confirm that the vault unlocks with the key held in the keystore:

```bash
./ztd vault unlock
```

### Checking Vault Status

Inspect whether a vault header exists and the lock state of the CLI process:

```bash
./ztd vault status
```

### Re-keying the Vault

Re-wrap the DEK with a new vault key and atomically commit the new sealed key (stop the runtime first):

```bash
./ztd vault rekey
```

### Resetting the Vault

Destroy the vault header, the canonical database and its WAL and SHM files, and the sealed vault key (`.ztd/secrets/vault_key`). The command requires typing `destroy` unless `--confirm` is passed:

```bash
./ztd vault reset --confirm
```

`vault reset` leaves the other keystore-sealed secrets in `.ztd/secrets/` in place. Recreate the whole runtime with `./ztd gw clean` instead; [Upgrading a v1.0.x Gateway](#upgrading-a-v10x-gateway) shows the full sequence.

### Upgrading a v1.0.x Gateway

A Gateway whose launch profile (`.ztd/pids/operator-launch-profile.json`) contains `master_key_file` cannot restart: the launch-profile decoder disallows unknown fields, so `./ztd gw restart` fails with `ErrLaunchProfileCorrupted`. Secrets sealed under a different master key cannot be opened with the key the current code selects. Recreate the runtime rather than resetting only the vault.

Load the host's key source first (on Linux without a Secret Service keyring, `eval "$(ssh-agent)" && ssh-add`), then run:

```bash
./ztd gw clean --yes
./ztd gw start
./ztd login
```

`gw clean` stops the Gateway and renames `.ztd` to `.ztd-<MMDDHHMM>`, which keeps the old state on disk for manual recovery. `gw start` initializes a new vault under the selected key source. The old launch profile, which records the Gateway's previous `config` (including `posture`, ports, and `vault_dir`), remains readable as `pids/operator-launch-profile.json` inside the renamed directory. `ztd login` signs the CLI in again because the CLI identity is recreated.

## Anti-patterns

- **Hardcoding encryption keys**: Storing plaintext private keys or DEKs in configuration files or source code.
- **Bypassing platform keystore for CA keys**: Writing unencrypted CA or service private key files to disk outside the keystore.
- **Relying on CLI vault status for Gateway state**: Expecting `./ztd vault status` to report the running Gateway's memory state rather than the CLI process state.
- **Assuming UEI rehydration is automatic for all scrubbed data**: Expecting scrubbed text that is not a UEI token (for example the `{{UEI_UNAVAILABLE}}` marker), or a token another runtime minted, to rehydrate automatically at execution time.
- **Sharing token mappings across runtimes**: Copying UEI mappings to another runtime so its tokens resolve there. That spreads plaintext; a foreign token stays opaque in `INFERENCE` and fails closed elsewhere.
- **Allowing unverified execution when rehydration fails**: Failing to fail closed when `rehydratePayload` cannot resolve a UEI token.
- **Editing generated crypto code or docs out of sync**: Changing crypto algorithms without updating owning packages and documentation.

## Links out

- [Authentication & Authorization Architecture](auth.md): Route classification, identity binding, passkey enrollment, and session lifecycle.
- [Governance Architecture](governance.md): Five-layer verification interlock, L5 actuator dispatch, and receipt signing.
- [Network Architecture](network.md): PKI hierarchy, trust bundles, SPIFFE identities, and TLS surfaces.
- [Storage Architecture](storage.md): Database topology, the execution record schema, and retention policies.
- [FIPS 140-3 Module Linkage and Scope](../reference/fips140-3.md): The validated boundary of the Go Cryptographic Module that ztd links (ztd is not separately validated), approved services, and runtime verification.
- [Troubleshooting Guide](../devs/troubleshooting.md): Recovery procedures for locked vaults and keystore failures.
