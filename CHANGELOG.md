# Changelog

All notable changes to this project are documented in per-release notes under [`docs/release_notes/`](docs/release_notes/).
This file serves as an index — each entry links to the full release notes for details.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## Unreleased

- Windows one-command onboarding: `irm https://zerotrustdata.io/start.ps1 | iex` (`website/static/start.ps1`) installs, starts a Gateway, signs in, and runs a first governed action; the homepage quick start and Releases pages show it as a first-class option.

## v1.0.x

| Version | Date | Description | Notes |
|---------|------|-------------|-------|
| 1.0.6 | 2026-10-13 | Cross-platform host support and tooling: Windows Job Object process management and smoke test isolation, macOS Keychain approval tolerance, host RAM observation on Darwin, port probe conflict detection, eval scenario workspace path normalization, and R2 credentials preflight verification. | [v1.0.6](docs/release_notes/v1.0.x/v1.0.6.md) |
| 1.0.5 | 2026-10-13 | Onboarding and host configuration: owner-only repo-root `.env` loaded by every `./ztd` command, `ZTD_OLLAMA_ENDPOINT`, `make up-public`/`make gw-public`, `gw start` fails on a running Gateway with different startup flags, Operators refuse a Gateway runtime tree, one-command onboarding (`start.sh`), and Releases pages on zerotrustdata.io. | [v1.0.5](docs/release_notes/v1.0.x/v1.0.5.md) |
| 1.0.4 | 2026-10-13 | First published 1.0 release: everything in 1.0.0, plus fixes to release verification, release rehearsal, R2 publication, and the CI deployment lifecycle. | [v1.0.4](docs/release_notes/v1.0.x/v1.0.4.md) |
| 1.0.3 | 2026-10-13 | Unpublished release attempt: release workflow failed uploading the download set to R2; fixed in 1.0.4. | [v1.0.3](docs/release_notes/v1.0.x/v1.0.3.md) |
| 1.0.2 | 2026-10-13 | Unpublished release attempt: release workflow failed re-verifying the downloaded artifact set; fixed in 1.0.4. | [v1.0.2](docs/release_notes/v1.0.x/v1.0.2.md) |
| 1.0.1 | 2026-10-13 | Unpublished release attempt: tagged without a matching `VERSION`; fixed in 1.0.4. | [v1.0.1](docs/release_notes/v1.0.x/v1.0.1.md) |
| 1.0.0 | 2026-10-12 | Five-layer governance pipeline (Gateway PDP, outbound Operator PEP, signed local receipts), protocol contracts, browser Console, Gateway-less autochthon, evaluation and signed compliance evidence tooling, and commit-hygiene guards. | [v1.0.0](docs/release_notes/v1.0.x/v1.0.0.md) |
