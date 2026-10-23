---
doc_id: autochthon
title: Run an Autochthon
audience: platform operators and host owners
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - docs/guides/autochthon.md
  - internal/cli/cmd/autochthon/
  - internal/services/autochthon/
related:
  - docs/architecture/operator.md
  - docs/guides/air_gap.md
  - docs/guides/connect_operator_to_gateway.md
when_to_read: Running `ztd autochthon` on a host with or without a Gateway, reading its status report, and deciding what its tally can and cannot show.
do_not_use_for:
  - The autochthon mechanism, culture, and tally format (docs/architecture/operator.md#autochthon-subsystem)
  - Governed execution, receipts, and the commitment ledger (docs/architecture/governance.md)
  - Operator enrollment and startup (docs/guides/connect_operator_to_gateway.md)
---

# Run an Autochthon

## Purpose

Describes running `ztd autochthon` on a host, reading `ztd autochthon status`, recovering from errors it reports, and interpreting its tally. The autochthon samples the host it runs on and breeds a local predictor of that host, with no Gateway, no enrollment, and no network listener. The mechanism is summarized in [Operator Architecture: Autochthon Subsystem](../architecture/operator.md#autochthon-subsystem).

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Choose a runtime root](#choose-a-runtime-root)
  - [Start the autochthon](#start-the-autochthon)
  - [Read the status report](#read-the-status-report)
  - [Interpret skill and surprises](#interpret-skill-and-surprises)
  - [Stop, restart, and resume](#stop-restart-and-resume)
  - [Run under a supervisor](#run-under-a-supervisor)
  - [Anchor the tally head](#anchor-the-tally-head)
  - [Recover from status and start errors](#recover-from-status-and-start-errors)
  - [Usage patterns](#usage-patterns)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

### Boundary (`INV-AUTO-BND`)

| ID | Rule |
| :--- | :--- |
| INV-AUTO-BND-01 | `ztd autochthon` is outside the governance boundary in every posture. It accepts no `GovernanceEnvelope`, runs no L1–L5 layer, opens no socket, and has no ingress path. It performs no mutation on the host beyond writing its own runtime files. |
| INV-AUTO-BND-02 | `ztd autochthon` accepts only `--working-dir` and `--log`; `ztd autochthon status` accepts only `--working-dir`, `--generations`, `--surprises`, `--anchor`, and the global `--json`. Any other set flag, including inherited `--endpoint` and `--port`, fails with `autochthon does not dial a gateway` before any `.ztd/autochthon/` file is opened. |
| INV-AUTO-BND-03 | The autochthon has no cryptographic identity. It does not enroll, holds no certificate or key, and does not sign its tally. |

### Runtime state (`INV-AUTO-STATE`)

| ID | Rule |
| :--- | :--- |
| INV-AUTO-STATE-01 | State lives only under `.ztd/autochthon/` in the working directory: `tally.jsonl`, `culture.bin`, and `run.lock`, mode `0600`, in directories of mode `0700`. An omitted `--working-dir` selects the current directory. |
| INV-AUTO-STATE-02 | `ztd autochthon status` writes nothing. On a root with no `.ztd/` it reports an empty tally and creates no directory. |
| INV-AUTO-STATE-03 | `culture.bin` is written only at a generation boundary, every 32 samples. Samples after the last boundary are in the tally but not in the culture, so a restart repeats that generation. |
| INV-AUTO-STATE-04 | One root admits one running autochthon. Start takes an exclusive OS lock on `run.lock` before it reads the tally and holds it until exit; a second start on the same root fails with `autochthon already running on this root`. `status` takes no lock. |

### Evidence limits (`INV-AUTO-EVID`)

| ID | Rule |
| :--- | :--- |
| INV-AUTO-EVID-01 | The tally is a local SHA-256 hash chain from a genesis hash. It is not the commitment ledger, not an `ActionReceipt`, and `ztd audit verify` does not walk it. |
| INV-AUTO-EVID-02 | Chain verification detects an edited, reordered, or inserted line. `status` reports an unterminated last line as pending and does not verify it, and start refuses to run on it. Chain verification does not detect removal of whole trailing lines without an external anchor verified via `status --anchor`. |
| INV-AUTO-EVID-03 | Sensed metrics (load, memory, process count) come from `/proc/loadavg` and `/proc/meminfo`. On platforms where those files are missing, those senses read as zero. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| CLI commands and accepted flags | `internal/cli/cmd/autochthon/` | `./ztd autochthon --help`, `./ztd autochthon status --help` |
| Sampling, culture, tally, and status report | `internal/services/autochthon/` | `./ztd test unit --pkg ./internal/services/autochthon` |
| Runtime paths | `internal/constants/paths.go` | Files under `.ztd/autochthon/` |
| Sentinel errors | `internal/constants/errors.go` | `ErrAutochthonGatewayFlag`, `ErrAutochthonCultureFormat`, `ErrAutochthonTallyBroken`, `ErrAutochthonRunning`, `ErrAutochthonAnchorMissing`, `ErrAutochthonAnchorFormat` |

## Procedures

### Choose a runtime root

Give the autochthon an isolated directory and specify it via `--working-dir`:

```bash
mkdir -p /var/lib/ztd-autochthon
```

Check initial status:

```bash
./ztd autochthon status --working-dir /var/lib/ztd-autochthon
```

### Start the autochthon

```bash
./ztd autochthon --working-dir /var/lib/ztd-autochthon
```

The process runs in the foreground, sampling host senses once per second and breeding a new generation every 32 seconds.

### Read the status report

```bash
./ztd autochthon status --working-dir /var/lib/ztd-autochthon
```

To emit machine-readable JSON:

```bash
./ztd autochthon status --working-dir /var/lib/ztd-autochthon --json
```

### Interpret skill and surprises

- **Skill**: Mean champion fit minus mean persistence fit over the scored samples. Positive skill means the culture predicts the host better than assuming nothing changes. A skill of `+0.0000` means the champion predicts no better than persistence.
- **Surprises**: Samples where champion fit dropped below 0.9. Each surprise row shows the load, memory, and process-count senses at that sample.

### Stop, restart, and resume

Send `SIGINT` or `SIGTERM` to stop the autochthon cleanly. Restarting with the same `--working-dir` verifies the existing hash chain, resumes from the last head, and loads the culture from `culture.bin`.

### Run under a supervisor

Example systemd unit file (`/etc/systemd/system/ztd-autochthon.service`):

```ini
[Unit]
Description=ztd autochthon host predictor

[Service]
ExecStart=/usr/local/bin/ztd autochthon --working-dir /var/lib/ztd-autochthon
Restart=on-failure
User=ztd

[Install]
WantedBy=multi-user.target
```

### Anchor the tally head

To detect truncation or removal of trailing lines, record the `Anchor` row from `status` externally, store it in the `ANCHOR` shell variable, and verify it on subsequent checks:

```bash
./ztd autochthon status --working-dir /var/lib/ztd-autochthon --anchor "$ANCHOR"
```

The anchor is in the form `<lines>:<64 lowercase hex>`. When the verified chain still contains the anchored head at line `<lines>`, the report prints `<lines>:<hash> holds`. A pending line never satisfies an anchor.

### Recover from status and start errors

| Error | Cause | Recovery |
| :--- | :--- | :--- |
| `autochthon does not dial a gateway: --<flag>` | A Gateway-related flag was passed. | Remove Gateway flags (`--endpoint`, `--port`). |
| `autochthon tally hash chain is broken: line N` | Tally line modified or corrupted. | Move corrupted `.ztd/autochthon/tally.jsonl` aside to begin a fresh chain. |
| `autochthon tally hash chain is broken: line N is unterminated` | Partial line written before a crash. Start refuses it; `status` reports it as pending. | Remove the trailing partial line from `tally.jsonl`. |
| `autochthon already running on this root` | Another instance holds `run.lock`. | Stop the other instance or specify a distinct `--working-dir`. |
| `autochthon culture file does not match this binary` | Binary version or genome format changed. | Remove `.ztd/autochthon/culture.bin` to restart culture at generation 0. |
| `autochthon tally does not contain the anchor` | The verified chain has fewer lines than the anchor, or no longer contains the anchored head. | Restore `tally.jsonl` from a copy that still contains the anchored line. |
| `autochthon anchor must be <lines>:<64 lowercase hex>` | The `--anchor` value is malformed. | Pass the `Anchor` row from `status` unchanged. |

### Usage patterns

- **Host baselining**: Run autochthon to establish normal host dynamics before enrolling an Operator.
- **Disconnected or air-gapped monitoring**: Run autonomously without network connectivity or credentials.
- **Telemetry verification**: Use `status --json` to export host prediction statistics into external logging frameworks.

## Anti-patterns

- Running without `--working-dir` inside a Gateway or Operator directory (INV-AUTO-STATE-01).
- Treating the autochthon tally as a cryptographic execution receipt or governance audit log (INV-AUTO-EVID-01).
- Assuming `chain verified` guarantees full history retention without checking an external anchor (INV-AUTO-EVID-02).
- Treating surprises as attribution of specific user actions or security violations (INV-AUTO-BND-01).

## Links out

- [Operator Architecture: Autochthon Subsystem](../architecture/operator.md#autochthon-subsystem) — Autochthon subsystem summary and state layout.
- [Governance Architecture](../architecture/governance.md) — The five-layer governance pipeline.
- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Seating hosts as governed execution targets.
- [Air-Gapped Deployment](air_gap.md) — Staging binaries in disconnected environments.
