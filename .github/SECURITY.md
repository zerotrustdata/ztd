# Security Policy

**Project:** ztd — zero-trust execution and evidence platform  
**Maintained by:** [Lateralus Labs](https://lateraluslabs.com)

---

## Supported Versions

| Version | Supported |
|---|---|
| `v1.0.x` | Yes — security fixes ship as `v1.0.x` patch releases; the latest patch is the supported one |
| Versions other than `v1.0.x` | No — unsupported; upgrade to the latest `v1.0.x` |

Security fixes land on `main` and ship in the next `v1.0.x` patch release.

---

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report privately to: **security@lateraluslabs.com**

Include as much of the following as you can:

- Description of the vulnerability and its potential impact
- Affected component(s): ztd Gateway (PDP), ztd Operator (PEP), ztd Protocol
- Steps to reproduce or a minimal proof-of-concept
- Your assessment of severity (Critical / High / Medium / Low)
- Whether you believe the issue is currently being exploited

We will acknowledge receipt within **48 hours** and provide an initial assessment within **5 business days**.

---

## Disclosure Policy

ztd follows **coordinated disclosure**:

1. You report privately to us.
2. We confirm, assess, and develop a fix.
3. We release the fix and credit you (unless you prefer anonymity).
4. You may publish after the fix is released, or after **90 days** from initial report — whichever comes first.

We will not pursue legal action against researchers acting in good faith under this policy.

---

## Scope

### In Scope

- **ztd Gateway (PDP)** — `GovernanceEnvelope` parsing, deterministic transaction hash binding, L1-L4 verification logic (Doctrine, Consensus, Notary, Warden), WebAuthn/Passkey L3 brokerage
- **ztd Operator (PEP)** — execution boundary (L5 Actuator), signed `ActionReceipt` issuance, mTLS tunnel, `SQLAuditStore`, Sovereign Execution Boundary (scrubbing, rehydration)
- **ztd Protocol** — Protobuf schemas (`common.proto`, `operator.proto`), canonical JSON (protojson) serialization, envelope integrity
- **Authentication** — WebAuthn/FIDO2 L3 Notary flow, Ed25519 signature verification (L2 Consensus, L4 Warden, L5 Actuator), replay protection (Nonce)
- **CLI and bootstrap** — `ztd login`, mTLS credential and PKI handling

### Out of Scope

- Third-party model providers (Anthropic, OpenAI, etc.)
- Vulnerabilities in dependencies that have already been publicly disclosed and are pending upstream fix
- Social engineering or phishing attacks against Lateralus Labs employees
- Denial-of-service attacks without demonstrated security impact beyond availability

---

## Security Architecture Notes

The following are structural properties of ztd that define the intended security model:

- **Fail-closed by design.** Any universal check or posture-required verification failure at the L1-L4 layers (Doctrine, Consensus, Notary, Warden) drops the payload and writes an audit record. Optional L2 and L3 results remain audit evidence and do not gate execution. There is no fallback execution path.
- **Sovereign Execution Boundary.** The `ztd Operator` (PEP) acts as the sovereign boundary. It refuses to mutate host reality unless the transaction satisfies every L2 Consensus and L3 human-authorization proof required by the active posture.
- **No ambient execution authority.** No component holds standing permission to mutate state. Authority is granted strictly per-transaction via the `GovernanceEnvelope`, verified independently at the PEP.
- **Local audit sovereignty.** Raw forensic material is stored locally in the `SQLAuditStore`. The Sovereign Execution Boundary scrubs all outbound data before delivery to remote clients or AI systems.
- **Mandatory encryption at rest.** All storage services require an unlocked vault for initialization. Sensitive data (command stdout/stderr, file diffs, content) is encrypted at rest using AES-256-GCM with per-operation nonces. Encryption operations fail-closed if the vault is locked.
- **mTLS everywhere.** All platform communication (Operator-to-Gateway) requires mutual TLS. Unauthenticated or unverified connections are rejected.
- **State & Replay Protection.** Transactions are bound to a `state_merkle_root`, protected by a unique `nonce`, and carry a temporal `expires_at` deadline.

If your finding demonstrates a bypass of any of these properties, treat it as **Critical**.

---

## CVE and Dependency Scanning

ztd runs automated dependency scanning on every build. If you identify a dependency vulnerability that is not captured by the automated tooling, report it through the channel above.

---

## Hall of Fame

Researchers who responsibly disclose vulnerabilities are acknowledged in this section. Acknowledgment includes the researcher's name or handle, with the researcher's permission.

---

## Contact

**Security:** security@lateraluslabs.com  
**General:** hello@lateraluslabs.com  
**Website:** https://lateraluslabs.com
