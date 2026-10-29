# Evaluation artifacts (`eval/`)

Checked-in evaluation program data and templates live here. **Runtime** campaign state (provider freezes, per-model inventories, rollout progress) lives under `.ztd/eval/` on the campaign host and is gitignored.

## Checked-in program inventory

| Path | Purpose |
| --- | --- |
| `eval/base-model-inventory.json` | **Default genesis program inventory** — 111 init-campaign models (41 scored assignments per model under `default-suite`: 27 scenarios, each under only the roles that perform it; 4,551 cells total) |
| `eval/base-init-campaign-queue.json` | Template rollout queue (`status: pending`); 45 entries at 41 cells each (`cells_per_run: 41`). Every entry is a model in `base-model-inventory.json`; the other 66 inventory models are not yet listed, including `gemma4:12b`. `glm-5.3-air` and `qwen2.5:1b-rlcd` are HF intake entries (`rollout-intake-hf.json`) that are not in the base inventory. |
| `examples/eval/init-campaign-queue.example.json` | Minimal queue shape reference |
| `eval/formation-catalog.json` | Formation catalog for system-lane campaigns (`--formations`); loaded when present, otherwise built-in defaults apply |
| `eval/formation-catalog-overlay.json` | Project overlay of formation edits written by `ztd eval formations add` |
| `eval/investigations/` | Seeded-investigation scenarios (E2): case seed, workspace with one planted fault, outcome predicates, canary files, budget, persona labels. Loaded by `investigation.Builtin` |
| `eval/suites/consensus-screen.json` | Screening across all three roles: instructions, history grounding, file arguments, contradiction, untrusted instructions, and authorized observation; 6 scenarios, 10 cells per model |
| `eval/suites/consensus-safety.json` | Consensus-compatible security scenarios and an authorized read-only control; 8 scenarios, 16 model-role cells per repetition |
| `eval/suites/consensus-evidence.json` | History grounding, read-only tool selection and arguments, recovery, and contradiction handling; 8 scenarios, 15 model-role cells per repetition |

The base inventory is the canonical 111-model genesis program set. Campaign ID: `eval-genesis-homogeneous`. Digests are reference snapshots from the provider or public model manifests; re-freeze from your Ollama host before scored runs on release code. `eval/rollout-intake-priority.json` lists variant IDs that `ztd eval rollout add` orders ahead of the rest of the backlog; it is currently empty, so adding uses the default ordering.

The optional Hugging Face/Ollama rollout intake (`eval/rollout-intake-hf.json`) records pull routes and pending provider adapters, including Qwen 2.5 1B RLCD and Nemotron Dory 4B. Trend selection favors small models that can generate text for the existing campaign harness; embedding, classification, image-generation, and audio-only models are excluded.

The inventory file is the authoritative model list and records the provider tags, digests, and family metadata. Per-model campaigns use `eval-init-<variant_id>` (legacy exception: `gemma4:e4b` → `init-campaign`). Each single-model run is **41** assignments under `default-suite`.

## Consensus qualification

The custom suites originated from `default-suite@1.22.0`. `consensus-screen@1.1.0` and `consensus-evidence@1.1.0` update the affected scenarios from `default-suite@1.23.0`: searches require complete fixture evidence and judged interpretation, upstream identity is judged by meaning and prompts no longer ask for masked names, and missing-file recovery requires a concrete safe next check. `consensus-safety@1.1.0` syncs its scenarios with `default-suite@1.23.0`, removing the masked-name prompt from `security-untrusted-runbook`. `consensus-screen` provides a short comparison across Primary, Assistant, and Lite. `consensus-safety` pairs seven refusal and bypass-resistance scenarios with `security-readonly-control`, so refusing every request cannot pass. `consensus-evidence` exercises allowed observations, receipt-backed history, argument correction, and evidence interpretation. These custom suites require no successful shell mutation, which the built-in default catalog excludes under L2-enforcing postures.

The prepared October 28 program covers six additional registry models with distinct model digests. Each has a screening campaign and a full `default-suite@1.22.0` campaign. All campaigns use one repetition with fresh investigations and the frozen `gemma4:e4b` judge. The judge is distinct from every subject. The Gateway's bound L2 consensus remains independent of the scored model or formation.

| Model | Served tag |
| --- | --- |
| Granite 4 3B-H | `granite4:3b-h` |
| LFM 2.5 1.2B Instruct | `hf.co/LiquidAI/LFM2.5-1.2B-Instruct-GGUF:Q4_K_M` |
| Spark X2.6 4B | `hf.co/XHToken/Spark-X2.6-4B-GGUF:Q4_K_M` |
| OLMo 3 7B Instruct | `olmo-3:7b-instruct` |
| Qwen3 4B Instruct-2507 | `qwen3:4b-instruct-2507-q4_K_M` |
| Nemotron 3.5 Bolt E4B | `hf.co/nvidia/NVIDIA-Nemotron-3.5-Bolt-E4B-4GB:Q4_K_M` |

Three formations are added to the project overlay. Every role binds to an installed registry entry with provider class `ollama`; Rnj-1 is excluded because it cannot run on this installation. Their new frozen system campaigns cover screening, safety, and evidence suites. A system assignment executes its designated eligible role from the formation; it does not automatically execute all three roles in sequence.

| Formation | Primary | Assistant | Lite | Estimated model and KV-cache memory / budget |
| --- | --- | --- | --- | --- |
| `consensus-spark-granite-liquid` | Spark X2.6 4B | Granite 4 3B-H | LFM 2.5 1.2B Instruct | 6,532 / 12,288 MiB |
| `consensus-qwen-granite-liquid` | Qwen3 4B Instruct-2507 | Granite 4 3B-H | LFM 2.5 1.2B Instruct | 6,279 / 12,288 MiB |
| `consensus-bolt-ministral-liquid` | Nemotron 3.5 Bolt E4B | Ministral 3 3B | LFM 2.5 1.2B Instruct | 8,755 / 16,384 MiB |

These estimates cover formation slots, not the independent judge and bound consensus models or a measured provider memory-fit result. The corrected rollout definitions are dry-run validated. No evaluations of the new Qwen3 campaigns or replacement formation campaigns have started; earlier screening and formation evidence is retained.

| Saved rollout | Campaigns | Assignments under Consensus |
| --- | --- | --- |
| `consensus-new-screen-20261028` | One screening campaign per model | 60 |
| `consensus-new-full-20261028` | One full campaign per model | 312 |
| `consensus-ollama-formations-20261028` | All three formations across screening, safety, and evidence | 123 |

The screening and full rollout definitions replace their Rnj-1 campaign with Qwen3 Instruct. The earlier `consensus-new-formations-20261028` definition also points to the replacement formation campaigns for future fresh executions. Use the new `consensus-ollama-formations-20261028` name for the corrected batch: resuming a historical execution preserves its original frozen campaigns, including Rnj-1. The old Rnj-containing formation was removed from the project overlay, and its active safety run was cancelled after its current assignment; its recorded evidence remains intact.

Run the saved rollouts from the repository root. Each executes serially with strict provider observation, model provenance, and evidence verification. Inspect the Gateway and consensus first: every run freezes the actual posture and bound consensus, and refuses to resume if either changes. See [Evaluation Programs](../docs/architecture/evals.md) for evidence and verification ownership.

```bash
export SSH_AUTH_SOCK=/run/user/1000/ztd-ssh-agent.sock
./ztd gw status
./ztd consensus status --json
./ztd eval rollout run consensus-new-screen-20261028
./ztd eval rollout run consensus-new-full-20261028
./ztd eval rollout run consensus-ollama-formations-20261028
```

Keep the bound L2 vote model constant across a comparison. Repeat a saved rollout to allocate fresh runs, or use `ztd eval rollout resume` to continue an interrupted execution. To run only its first campaign, add `--until 1`. A later suite revision requires a new campaign ID to preserve earlier evidence. To use the checked-in fairness fixes, update the registered screening/evidence suites with `ztd eval suites update eval/suites/consensus-screen.json` and `ztd eval suites update eval/suites/consensus-evidence.json`, then create new campaigns and a new named rollout. Updating a registered suite does not replace any campaign’s frozen copy. Existing rollout resumes retain their original criteria; they do not adopt the new suite revision. On a fresh runtime, register the custom suites with `ztd eval suites create`, then freeze campaigns with `ztd eval campaigns create`; the saved campaigns and rollout definitions live in the local evaluation store.

A verified run means its evidence passes verification; its models can still fail tasks. Review assignment verdicts, failed grades, and sealed traces with `ztd eval runs assignments` and `ztd eval runs trace`, and correlate transaction and provider-attempt IDs with `ztd gw logs`. Report invalid evidence, provider failures, and missing witnesses separately from observed model failures. Preserve the original runs when fixing an evaluator bug, and execute fresh runs to validate the correction.

## Small-model catalog refresh (2026-10-24)

Added 17 catalog variants and matching intake routes. Public manifest SHA-256 digests are reference identities, not evidence that these models are installed or evaluated. Existing provider observations remain unchanged; new entries carry no capability observations. Pull selected entries through `ztd eval models pull`, then freeze the live provider before campaigns. The checked-in rollout queue remains a 45-entry template. All 17 additions were subsequently downloaded through the governed Inference Operator on 2026-10-24. The live provider freeze contains 111 models and confirms that all 17 installed digests match their catalog references. Larger transfers required resumptions after the Gateway's 30-second result timeout. Capability probes and scored evaluations have not been run for these additions.

- **Spark-X2.6-4B Q4_K_M** — Trending on-device agent model; publisher requires Ollama v0.34.1+. [Publisher card](https://huggingface.co/XHToken/Spark-X2.6-4B)
- **Nemotron 3.5 Bolt E4B Q4_K_M** — Trending Gemma 4 derivative for multi-turn tool use; evaluate text inputs. [Publisher card](https://huggingface.co/nvidia/NVIDIA-Nemotron-3.5-Bolt-E4B-4GB)
- **LFM2.5 1.2B Instruct and Thinking; 8B-A1B** — Small hybrid instruction, reasoning, and tool-use variants. [Instruct](https://huggingface.co/LiquidAI/LFM2.5-1.2B-Instruct), [Thinking](https://ollama.com/library/lfm2.5-thinking), [8B-A1B](https://ollama.com/library/lfm2.5:8b)
- **Rnj-1 8B** — Code, STEM, and tool use. [Ollama card](https://ollama.com/library/rnj-1)
- **OLMo 3 7B Instruct and Think** — Independent instruction and reasoning checkpoints with open training artifacts. [Ollama card](https://ollama.com/library/olmo-3)
- **FunctionGemma 270M** — Function-calling specialist intended for task-specific fine-tuning, rather than general dialogue. [Ollama card](https://ollama.com/library/functiongemma)
- **Granite 4 350M, 1B-H, 3B-H, 7B-A1B-H** — Tiny instruction model and hybrid/MoE tool-use comparisons. [Ollama card](https://ollama.com/library/granite4)
- **Qwen3 4B Instruct-2507 and Thinking-2507 Q4_K_M** — Explicit post-training checkpoints, separate from the existing generic 4B tag. [Instruct](https://huggingface.co/Qwen/Qwen3-4B-Instruct-2507), [Thinking](https://huggingface.co/Qwen/Qwen3-4B-Thinking-2507)
- **DeepSeek R1 1.5B and 8B** — Small reasoning distillations; the 8B tag is R1-0528-Qwen3, separate from the existing Qwen2-based 7B. [Ollama card](https://ollama.com/library/deepseek-r1)

Parameter counts follow the selected artifact's registry metadata, not its marketing tag or active-parameter count. Fourteen additions are at most 8B by that metadata; Rnj-1 (8.3B), LFM2.5-8B-A1B (8.5B), and DeepSeek R1 8B (8.2B) are nearby size-class additions. Bolt's quantized text artifact reports 7.52B; its publisher describes the complete model as 8B total / 4B effective. Spark's GGUF reports 4.45B, versus 4.11B in the BF16 checkpoint. These counts and advertised context limits need confirmation during provider freeze; a maximum context window is not a memory-fit guarantee. LFM2.5 1.2B entries use the publisher's 32,768-token trained context.

Nemotron Dory 4B is an additional **pending intake entry only**. Its publisher supplies a GGUF, but the [model card](https://huggingface.co/nvidia/NVIDIA-Nemotron-Labs-Dory1.0-4B-BF16) requires a Dory-specific llama.cpp branch. It is skipped by automated staging until provider support is available; no catalog digest or passing capability result is invented.

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

# Failed models stay excluded until explicitly retried after repair.
./ztd eval rollout retry <tag>

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
| `ztd eval runs start` | Start a campaign run with witness, verify, and publish flags. Each run freezes the live Gateway posture and consensus binding when it starts and prints them |
| `ztd eval runs export` | Export one run's disclosure-safe bundle under the runtime root (`--output-dir`, runtime-relative) |
| `ztd eval rollout add` | Add models to `.ztd/eval/init-campaign-queue.json`. Skips and lists a model that shares a digest with a queued model or with one earlier in rollout order (an alias). Capability probes never exclude a model |
| `ztd eval rollout run` | Unattended rollout: start → strict-witness verify for queued models. A run in which the provider failed every assignment fails the entry as `provider_failure (<class>)`, for example `(capability_unsupported)` for a model without tool support; stopping a run ends the batch and leaves the entry pending. `--gate-smoke` screens with the smoke suite first; `--promote-on-pass` follows a smoke pass with the full run. The batch holds the queue lock: `rollout add`/`remove`/`retry`/`skip` fail until it ends. Model-driven runs refuse any Data Operator whose working directory is, contains, or sits inside the project, except a throwaway Operator directly under `.ztd/operators/<name>` (the embedded Operator included) (INV-EVAL-CAMP-32) |
| `ztd eval rollout list` | Inspect queue entries |
| `ztd eval rollout next` | Show the next pending model |
| `ztd eval rollout retry` | Reset entries back to pending |
| `ztd eval rollout skip` | Skip queue entries |
| `ztd public restore` | Republish verified runs to the gateway-owned public mirror |

Rollouts stop a model's run after three consecutive provider `generation_failed` results, record the entry as failed, and continue to the next model. The failed attempts retain their signed evidence; unexecuted assignments stay queued for explicit inspection or resume. Successful responses and other failure classes reset the consecutive-failure count. Both `rollout run` and `rollout resume` exclude failed entries until `rollout retry` returns them to pending, including when `--skip-verified=false` is used. Evidence verification remains separate from model performance grading.

See [Evaluations architecture](../docs/architecture/evals.md) for full campaign operations.

### Run governance freeze

- A campaign does not freeze a governance posture; each run freezes the live Gateway posture and consensus binding at start. `runs execute` and `runs resume` refuse a run once either differs from what it froze (`ErrEvaluationRunGovernanceChanged`).
- Runs started before v1.3.0 froze no consensus binding. They cannot be executed or resumed, and they stay in campaign history. Start a new run of the same campaign. See [v1.3.0 release notes](../docs/release_notes/v1.3.x/v1.3.0.md).

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
