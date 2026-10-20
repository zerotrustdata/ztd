# Evaluation artifacts (`eval/`)

Checked-in evaluation program data and templates live here. **Runtime** campaign state (provider freezes, per-model inventories, rollout progress) lives under `.ztd/eval/` on the campaign host and is gitignored.

## Checked-in program inventory

| Path | Purpose |
| --- | --- |
| `eval/base-model-inventory.json` | **Default genesis program inventory** — 94 init-campaign models (41 scored assignments per model under `default-suite`: 27 scenarios, each under only the roles that perform it; 3,854 cells total) |
| `eval/base-init-campaign-queue.json` | Template rollout queue (`status: pending`); 45 entries at 41 cells each (`cells_per_run: 41`). Every entry is a model in `base-model-inventory.json`; the other 49 inventory models are not yet listed, including `gemma4:12b`. `glm-5.3-air` and `qwen2.5:1b-rlcd` are HF intake entries (`rollout-intake-hf.json`) that are not in the base inventory. |
| `examples/eval/init-campaign-queue.example.json` | Minimal queue shape reference |
| `eval/formation-catalog.json` | Formation catalog for system-lane campaigns (`--formations`); loaded when present, otherwise built-in defaults apply |
| `eval/formation-catalog-overlay.json` | Project overlay of formation edits written by `ztd eval formations add` |
| `eval/investigations/` | Seeded-investigation scenarios (E2): case seed, workspace with one planted fault, outcome predicates, canary files, budget, persona labels. Loaded by `investigation.Builtin` |

The base inventory is the canonical 94-model genesis program set. Campaign ID: `eval-genesis-homogeneous`. Digests are a reference provider snapshot; re-freeze from your Ollama host before scored runs on release code. `eval/rollout-intake-priority.json` lists variant IDs that `ztd eval rollout add` orders ahead of the rest of the backlog; it is currently empty, so adding uses the default ordering.

The optional Hugging Face rollout intake (`eval/rollout-intake-hf.json`) also tracks the small text-generation trend candidate Qwen 2.5 1B RLCD. Larger trend entries are intentionally excluded from this 16 GiB-provider intake; classification, image, audio, and image-text-to-text repositories from the same trend snapshot are also not added to the text-generation queue.

The inventory file is the authoritative model list and records the provider tags, digests, and family metadata. Per-model campaigns use `eval-init-<variant_id>` (legacy exception: `gemma4:e4b` → `init-campaign`). Each single-model run is **41** assignments under `default-suite`.

## Runtime files (campaign host)

| Path | Purpose |
| --- | --- |
| `.ztd/data/eval/runs/<run-id>/` | **Canonical campaign evidence** — assignments, verification, publication state (survives public mirror wipe) |
| `.ztd/eval/model-inventory.json` | Full provider freeze from `ztd eval models freeze` |
| `.ztd/eval/inventories/*.json` | Per-model campaign inventory files |
| `.ztd/eval/init-campaign-queue.json` | Rollout queue manifest |

Gateway-owned public mirror state (SSE feed, explorer datasets) lives under the Gateway runtime tree in `.ztd/public-mirror/` and `.ztd/public-feed/`. It is separate from the campaign run store. After a mirror wipe, republish from host run artifacts (see [Wipe recovery](#wipe-recovery)).

## Typical workflow

```bash
# 0. (Optional) Pull trending Hugging Face GGUF models through governed Operator dispatch.
#    Catalog: eval/rollout-intake-hf.json. Applies canonical served-model aliases
#    (for example qwen3.8:27b, glm-5.3-flash) after pull/copy.
./ztd eval models pull --all
# Manual-create entries (sharded GGUF, pending single-file) are skipped and need a
# manual `ollama create` from their Hugging Face GGUF.

# 1. Freeze provider inventory into runtime registry. --probe-capabilities records
#    governed capability observations for diagnostics. Probes never exclude a
#    model or remove tools: every evaluation records the current provider response.
./ztd eval models freeze --probe-capabilities

./ztd eval models list

# Inspect saved observations. A plain freeze replaces the registry without probes.
./ztd eval models show smollm2:135m

# 2. Build rollout queue from catalog models (default)
./ztd eval rollout add --all

# rollout run defaults: witness, verify, publish, daemon, and skip-verified are all true
./ztd eval rollout run
# ./ztd eval rollout run --until 5

# A model-role campaign freezes exactly one model; queue several for rollout
./ztd eval rollout add qwen3:0.6b
./ztd eval rollout add qwen3:4b
./ztd eval rollout add gemma3:4b
./ztd eval rollout run
```

## CLI reference

| Command | Purpose |
| --- | --- |
| `ztd eval models freeze` | Discover and freeze provider models into the runtime registry |
| `ztd eval models list` | List variants in the catalog or registry; `--max-params` and `--family` filter, and `--detailed` adds parameters, family, quantization, and digest columns |
| `ztd eval models add` | Add or update one variant in the registry (or `--catalog`) |
| `ztd eval models remove` | Remove one variant by selector from the registry (or `--catalog`) |
| `ztd eval models import` | Copy variants from the catalog into the runtime registry |
| `ztd eval models pull` | Dispatch governed pull/copy maintenance to the exact Inference Operator session |
| `ztd eval campaigns create` | Create a frozen campaign definition over one registry model |
| `ztd eval runs start` | Start a campaign run with witness, verify, and publish flags |
| `ztd eval runs export` | Export one run's disclosure-safe bundle under the runtime root (`--output-dir`, runtime-relative) |
| `ztd eval rollout add` | Add models to `.ztd/eval/init-campaign-queue.json`. Skips and lists a model that shares a digest with a queued model or with one earlier in rollout order (an alias). Capability probes never exclude a model |
| `ztd eval rollout run` | Unattended rollout: start → strict-witness verify for queued models. A run in which the provider failed every assignment fails the entry as `provider_failure (<class>)`, for example `(capability_unsupported)` for a model without tool support; stopping a run ends the batch and leaves the entry pending. `--gate-smoke` screens with the smoke suite first; `--promote-on-pass` follows a smoke pass with the full run. The batch holds the queue lock: `rollout add`/`remove`/`retry`/`skip` fail until it ends. Model-driven runs refuse any Data Operator whose working directory is, contains, or sits inside the project, except a throwaway Operator directly under `.ztd/operators/<name>` (the embedded Operator included) (INV-EVAL-CAMP-32) |
| `ztd eval rollout list` | Inspect queue entries |
| `ztd eval rollout next` | Show the next pending model |
| `ztd eval rollout retry` | Reset entries back to pending |
| `ztd eval rollout skip` | Skip queue entries |
| `ztd public restore` | Republish verified runs to the gateway-owned public mirror |

See [Evaluations architecture](../docs/architecture/evals.md) for full campaign operations.

## Wipe recovery

What survives depends on what you wipe:

| Wipe scope | Host `.ztd/data/eval/runs/` | Host `.ztd/eval/` queue | Gateway public mirror |
| --- | --- | --- | --- |
| Mirror state only (`.ztd/public-mirror/`, `.ztd/public-feed/`) | **Kept** | **Kept** | **Lost** |
| `./ztd gw clean` / full `.ztd` delete | **Lost** unless backed up (`gw clean` renames `.ztd` aside) | **Lost** unless backed up | **Lost** |

### After public mirror wipe

Host evidence and queue remain. Restore the public mirror from verified run store:

```bash
curl -sf http://127.0.0.1:8082/bootstrap | jq '{freshness: .source_freshness, high_water: .snapshot.high_water_sequence}'

./ztd public restore --queue
```

For one run:

```bash
./ztd public restore eval-init-granite3-3-2b-1789754079
```

If `runs publish` exports zero records after a mirror wipe (the Gateway-owned publication state still lists old idempotency keys), use `--force` on publish instead — see [Wipe recovery](#wipe-recovery).

Runs marked `verified` in the queue but missing under `.ztd/data/eval/runs/<verified_run_id>/` cannot be mirror-restored. Mark them pending and re-run:

```bash
./ztd eval rollout retry qwen3-0-6b
./ztd eval campaigns create eval-init-qwen3-0-6b qwen3:0.6b
./ztd eval runs start eval-init-qwen3-0-6b --publish --daemon --require-witness
```

### Export and back up evidence

Canonical evidence stays under `.ztd/data/eval/runs/` during execution, encrypted with the runtime vault. Export a disclosure-safe bundle (JSONL, CSV, SQLite) any time. `--output-dir` is relative to the runtime root, so this writes `.ztd/exports/granite-reference/`:

```bash
./ztd eval runs export eval-init-granite3-3-2b-1789754079 --output-dir exports/granite-reference
```

To keep the evidence itself across a full platform wipe, back it up to a directory outside the runtime tree with `./ztd eval backup` and restore it with `./ztd eval restore`. The snapshot holds the evidence encrypted, together with the vault key chain that sealed it. `./ztd eval restore` opens that chain with the host key store and reseals the evidence under the current runtime vault. `./ztd eval runs delete` and `./ztd eval campaigns delete` also purge the deleted evidence from `eval/backups/`, so a restore never brings it back.

## License

Source in this repository is licensed under the Business Source License 1.1 (BSL 1.1). It converts to Apache 2.0 on 2030-08-18. See the repository [LICENSE](../LICENSE) for details.
