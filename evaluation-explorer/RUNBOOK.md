# OpenDevOps.ai evaluation explorer

## Prerequisites

Run commands from the repository root unless a section changes directory. The repository `ztd` binary exists (`make build`), Node dependencies are installed in `evaluation-explorer`, the CLI has a valid `./ztd auth context`, and the approved Ollama provider is reachable at the endpoint passed to the Gateway as `--inference-ollama-endpoint`. Do not install, start, pull, or manage Ollama on this host. The native evaluation suite does not call Ollama.

On the **provider host** (where Ollama runs), run separate witness Operator sessions when running full campaigns, started with `./ztd operator start` (see `./ztd operator start --help`):

1. **Observer Operator** — `--capabilities observer` for provider-boundary resource observation.
2. **Provenance Operator** — `--capabilities provenance` with `--model-storage-root` pointing at the Ollama models directory (for example `~/.ollama/models`).

## Start and verify the platform

Set `OLLAMA_ENDPOINT` to the approved provider endpoint, including scheme, host, and port, before running the block.

```bash
make gw GW_ARGS="--inference-ollama-endpoint $OLLAMA_ENDPOINT"
./ztd auth enroll pending
make status
./ztd auth context
```

Gateway and Operators report healthy before a real evaluation. Approve or deny an exact pending workload request only through the documented `auth enroll` flow (`pending`, `approve`, `deny`); do not reset the trust domain to repair an enrollment wait.

## Start the complete mock experience

```bash
cd evaluation-explorer
npm run dev:mock
```

Open `http://127.0.0.1:5173`. This command requires the gateway-owned mirror on public read-only `127.0.0.1:8082` (start it with `./ztd gw start --public-spectator`; the command exits if the port is not listening). It publishes deterministic fixture history through `scripts/seed.mjs --fixtures`, which decides what is already present by reading the mirror's anonymous public history API on `8082`; if that API is unavailable, the seed fails with an error and publishes nothing. The supervisor logs the seed failure and continues with replay and Vite. It then replays a scripted lifecycle through the real publisher and mirror, and starts Vite. The browser connects only to `127.0.0.1:8082` with credentials omitted.

The Vite dev server and the Gateway explorer listener both default to `127.0.0.1:5173` (`--eval-explorer-listen` changes the Gateway address), so they cannot run on that port at once. A running Gateway already serves the embedded explorer on `127.0.0.1:5173`. To run Vite alongside it, set `ZTD_VITE_PORT` to a free loopback port and open that port instead; Vite runs with `--strictPort` on that port. The same port rule applies to `dev:real`.

## Start the real native evaluation experience

```bash
cd evaluation-explorer
npm run dev:real
```

Requires the gateway-owned mirror (`./ztd gw start --public-spectator`, which is the default; `make gw` starts it with the embedded Operator). Use `npm run health` to inspect mirror state, publisher sequence, freshness, route isolation, and the last accepted record.

Run a campaign evaluation with live publication to the public mirror. Set `CAMPAIGN_ID` to the new campaign identifier and `MODEL_TAG` to the frozen model tag, then run:

```bash
./ztd eval campaigns create "$CAMPAIGN_ID" "$MODEL_TAG" --suite smoke-suite
./ztd eval runs start "$CAMPAIGN_ID" --require-witness
```

This requires enabled Data, Inference, Observer, and Provenance capabilities. The Gateway embedded Operator can supply all of them (`make gw MODEL_STORAGE_ROOT=<model-store>`); separate outbound Operators are optional. Freeze the provider inventory and pass the chat acceptance gate first; see [Executing a Model Campaign](../docs/architecture/evals.md#executing-a-model-campaign). Provider access comes from the Inference Operator's enrolled configuration.

Resume a prepared or interrupted run, or catch up publication for an existing run. Set `RUN_ID` to the run identifier, then run:

```bash
./ztd eval runs resume "$RUN_ID" --publish --daemon
./ztd eval runs publish "$RUN_ID"
```

Runs started before v1.3.0 froze no consensus binding, so `runs resume` and `runs execute` refuse them. Start a new run of the same campaign instead; the older run stays in history. A run also refuses to resume or execute once the Gateway's posture or consensus binding differs from what it froze.

The Go `CampaignPublicationCoordinator` in the evaluation service projects canonical campaign state into public-safe explorer records and publishes them through the real `ztd public` publisher. The explorer's TypeScript `campaign-adapter` decodes those envelopes from the mirror. No separate projector script is involved.

## Assignment evidence and publication repair

Assignment Details shows the saved role's final response, every recorded tool call in execution order, exact arguments, approved arguments when they differ from the proposed arguments, resolved commands, tool results, outcomes, and trace digest through the existing bounded `role_transcripts` contract. Tool results start expanded. Distinct legacy response text also remains visible. Publication and JSONL/SQLite exports build transcripts from the digest-checked assignment agent trace; the verified result revision includes the same transcript instead of replacing it with a summary-only record. Missing referenced traces, digest failures, and oversized records fail publication rather than silently dropping the evidence.

The page shows every activity family, distinguishing zero observed records, unavailable evidence, and scenario inapplicability. Assignment event history retains every distinct loaded event ID independently of the Live page's 500-event buffer and its grouping of planned/scored invocations or result/verified revisions. The public mirror's own retention still bounds what history can be loaded. “Assignment feed events” exposes each loaded event's fields and downloads the event array. “Published assignment data” exposes the normalized assignment and the validated original public source record, including projection fields that summary components do not display. These public feed records and the existing feed-only audit slice are not the complete Operator audit ledger.

Older signed records do not gain transcripts when the frontend is rebuilt. After deploying the rebuilt binary, republish affected runs from their retained source evidence with `./ztd eval runs publish <run-id> --force`; confirm the latest result and verified-result records contain `role_transcripts`. This appends new signed projections and does not rewrite source evidence or old signed batches. A genuinely absent historical trace cannot be reconstructed from token counts. The page reports that the transcript was not published, without claiming the model generated no output.

An agent-executor assignment also publishes its complete sealed agent trace as a proof artifact, and Assignment Details renders it under "Full agent trace" once the body matches its published SHA-256 and its `evidence_digest`: the exact model input (messages and tool schemas), every turn's thinking, text, finish and signed receipt, every tool call's arguments, result and governed receipt, both runtimes' anchors, and the sentinel bracket. A body that fails either check shows the failure and none of its content. `agent-trace.v3` runs predate input capture; their input reads "not captured" and is never reconstructed. Download the trace from the section and verify it offline with the command it shows, `./ztd public verify-agent-trace --trace assignment-<id>.agent-trace.json`. Each criterion badge and the failure diagnosis show the grader's detail verbatim. "Scenario fixture" shows the exact scenario the campaign froze once each body matches its published SHA-256 and the assignment's scenario: the user prompt, inline context, workspace files (decoys marked), the seeded investigation, and the gold criteria the result was graded against, each downloadable. Runs published before these artifacts gain them through `./ztd eval runs publish <run-id> --force`.

## Optional disposable public-feed reset

This reset removes the gateway-owned feed generation and mirror state, creates a new local public signing chain, and republishes the selected seed. It does not remove evaluation report roots. Obtain explicit owner confirmation immediately before running it. Then stop the Gateway and remove `.ztd/public-feed`, `.ztd/public-proofs`, `.ztd/proof-mirror-sync.json`, and `.ztd/public-mirror`. The proof log is signed by the feed key, so it must go with that key; Gateway startup fails closed with `ErrPublicFeedOrphanedGeneration` if any of it remains. Start `./ztd gw start --public-spectator` and run:

```bash
cd evaluation-explorer
npm run dev:mock
```

`npm run dev:mock -- --reset` is no longer supported; `dev.mjs` refuses it.

Do not use `./ztd gw clean` for a public-feed problem.

## Publish evaluation reports

Publication is owned by the Go evaluation service. Campaign runs project lifecycle events, assignment results, and aggregate records into the public feed as they execute (`runs start` publishes by default; `runs resume` uses `--publish`) or on demand (`ztd eval runs publish <run-id>`). The publisher signs batches, writes the durable outbox, and advances the high-water sequence; the mirror ingests signed batches and serves anonymous reads and SSE.

## Expected browser states

The About page introduces the Explorer as a live view of ztd development and model evaluations. It explains the goals of testing the platform, selecting Primary/Assistant/Lite candidates, investigating small-model capability and evolving investigation context, comparing a frontier model alone with a supported model team, measuring accuracy/security/safety/privacy, and building inspectable evidence that ztd works. Context evolution and team improvements are research questions whose conclusions depend on comparable evidence. The page also presents the project as the builder’s working portfolio and provides contact links for team roles and contract work.

The Live landing page places a Runs column on the left, with the live event table directly to its right; at 1000px and below they stack with Runs first. Both panels resize to the available viewport above the footer, with independent scrolling inside the Runs list and event table instead of page scrolling. Mouse-wheel and touch scrolling stay in the hovered panel, and both lists support keyboard scrolling when focused. Panel headings, activity controls, and event pagination stay outside the scrolling lists. The table shows one compact row per assignment alongside run lifecycle activity: result, task, model and role, latency, input and output tokens, generation throughput, and event time. Metric headers carry units (`Latency (s)`, `Input (tok)`, `Output (tok)`, and `Tokens/s (tok/s)`); measured cells show numeric values without unit suffixes or compact abbreviations. Latency uses seconds with two decimal places and throughput uses one decimal place. Task names and result icons open assignment evidence (or run details for run-level activity); model names open model details. A check marks completion, an X marks a model failure, and a neutral exclamation marks an infrastructure or evidence outcome, with accessible result labels. Failure explanations remain in Assignment Details. Missing measurements display `--`, never zero; throughput requires observed output tokens and generation time. Progress and verification stay in Runs and the campaign header rather than repeating in every row.

All runs in the selected release remain visible, including events that precede summaries. The table shows up to 100 recent matching activity rows, paginated in groups of 25; Latest run includes all loaded rows for that run. The model control filters the activity; Filters exposes run, event, category, and assignment selection. Rows and their metrics update continuously as new feed records arrive. On small screens rows become stacked cards. The Live page has no explanatory panel below the stream; methodology and task definitions remain available through navigation and assignment details.

The overview first reconstructs bootstrap, complete paginated history, and the sealed snapshot, then connects SSE. A live run appears as queued, running, provisional assignment and metric updates, and finally completed, failed, or stopped without a refresh. Reload reconstructs the same state before SSE resumes. In Runs, model names link to their model definitions for the dataset and role; the run ID directly beneath the heading links to that run's evaluation details. Verified, not fully verified, legacy unverified, in-progress, failed, unavailable, and not-evaluated states remain distinct.

The Evaluations page defaults to all releases and all quality states, keeping active runs and failures visible beside verified history. Its tab counts reflect the active filters. Campaigns, evaluation runs, and the formation catalog use detailed tables with sortable column headers, column visibility controls, and pagination. Tables scroll horizontally on smaller screens and remain keyboard accessible. Campaign rows include run status counts, assignment progress, verifier counts, median pass rate, and the latest run time; their titles open the latest run details. Governance conformance and model runs remain in separate tables, with sortable status, progress, outcomes, timing, pass rate, and quality columns. Formation rows retain model links for Lite → Assistant → Primary, served tags, family, parameter count, quantization, provider, trust, per-role weights and KV cache, combined VRAM utilization, and diversity counts. The formation catalog is reference data for system-lane campaigns: `ztd eval campaigns create --formations` freezes the selected formations, and `ztd eval runs start` scores each one as a single unit against every scenario. Formation search and kind filters apply before sorting. Framework reference details are available in an expandable table above the summaries. Use its quality filter to inspect verified runs only.

Run detail follows the evidence from the run to assignments or conformance scenarios, verification, and the evidence bundle. Conformance scenario cards expand to versioned assertion verdicts; a rejected scenario is a correct refusal. Native metrics retain their published count or ratio units and numerator/denominator. Model verdicts (`completed`, `model_failed`) remain separate from infrastructure and evidence outcomes that are not counted for or against the model. Model and task detail link back to the assignments that supply their evidence; an unmeasured model says “Not evaluated.” Missing evidence is never rendered as a measured zero.

The Evals leaderboard admits only scored models with exactly 100% evaluation coverage and passing verification that publish p50 latency, output tokens per task, and an applicable security grade. Primary, Assistant, and Lite rank by Score, which orders accuracy, then security, then speed, then token cost: `100 × pass rate × √security × (0.85 + 0.10 × speed + 0.05 × tokens)`. Speed and tokens are the role's best (lowest) p50 latency and output tokens per task divided by the model's, so efficiency can lower a score by at most 15% and only separates models of similar accuracy and security. The square root gives security half of accuracy's weight, while a zero security grade still scores zero. Security uses security-task pass rates from assignments when available, otherwise the model's `security_resistance`. Roles without security-policy scenarios show N/A and use a factor of 1. Equal scores prefer higher accuracy, then smaller known size, then model name and variant ID. Size comes from the model tag or name (billions or millions of parameters) and is unavailable when neither advertises it. Statistical columns show raw values; size, tokens/s, and evaluation time (from a dataset containing one homogeneous model run) are displayed but not scored.

Overall ranks one row per model (`variant_id`) by `(Primary score + Assistant score + Lite score) / 3`, with equal weight for each role. Models must qualify in all three roles to enter Overall; models with fewer qualified roles remain available in their role tabs. Repeated datasets are averaged within each role first, so extra runs or assignments cannot increase one role's weight. The Overall tab omits the Role column, counts distinct models, and shows each role's score in the aggregate score tooltip. Accuracy, latency, output tokens, and throughput are means of role means, not pooled assignment statistics; security averages applicable role means. Missing throughput stays unavailable. Elapsed time sums distinct contributing datasets once and stays unavailable if any contributing duration is unknown. Switching tabs restores ranking by descending Score.

## Verification

```bash
cd evaluation-explorer
npm run lint
npm run typecheck
npm test
npm run build
npm run test:e2e
npm audit --audit-level=moderate
npm run health
```

For a platform production-code change:

```bash
make build
./ztd test unit
./ztd test lint
./ztd test coverage
```

Rebuild and restart the Gateway when the platform binary changed:

```bash
make build
make down && make gw GW_ARGS="--inference-ollama-endpoint $OLLAMA_ENDPOINT"
make status
```

## Production build and packaging

The checked-in local runtime remains loopback-only for development. `npm run build` replaces the emitted runtime with the exact bytes from `runtime.production.json`, disables source maps, scans the complete asset tree for prohibited content, and fails on any unexpected file. Production does **not** use Cloudflare Workers or Pages; the gateway-owned public listener on `8082` serves `dist/` and the anonymous mirror API from one origin.

```bash
cd evaluation-explorer
npm ci
npm run lint
npm run typecheck
npm test
npm run build
npm audit --audit-level=moderate
```

From the repository root, run `make explorer-embed` to refresh the committed explorer assets, then `make explorer-embed-check` to verify them and `make build` to compile them into the binary. `make ci-explorer` checks for drift without refreshing the embed. Restart the Gateway to serve the new binary. The production runtime names only `https://opendevops.ai`. External browsers reach that origin through the dedicated `opendevops-feed` Cloudflare Tunnel, which terminates at loopback `http://127.0.0.1:8082`.

## Host service supervision

The gateway owns the in-process public mirror (`8081` private ingest, `8082` public read/SSE) and evaluation explorer when started with `--public-spectator` (default). Do not install a host mirror systemd unit for production.

The checked-in `evaluation-explorer/deploy/systemd/opendevops-feed-tunnel.service` runs only the Cloudflare Tunnel on the Gateway host. It reads generated configuration and credentials from the owner-protected `~/.cloudflared` directory and forwards `https://opendevops.ai` to `http://127.0.0.1:8082`. Neither the tunnel unit nor the Gateway binary contains a token, key, or ingest credential in source control.

After the release owner creates the dedicated `opendevops-feed` tunnel and DNS route, install the tunnel unit when no other process owns the same tunnel session:

```bash
install -Dm600 evaluation-explorer/deploy/systemd/opendevops-feed-tunnel.service ~/.config/systemd/user/opendevops-feed-tunnel.service
systemctl --user daemon-reload
systemctl --user enable --now opendevops-feed-tunnel.service
systemctl --user status opendevops-feed-tunnel.service
```

User lingering must be enabled for restart-on-boot before login:

```bash
sudo loginctl enable-linger "$USER"
```

Stopping the tunnel preserves publisher and mirror state. Disable the tunnel service first if external route isolation or disclosure acceptance fails; do not reset feed state, rotate keys, or delete the tunnel as an ordinary rollback.

## Focused troubleshooting

- **Workload unhealthy:** run `./ztd auth enroll pending`, approve the exact pending request when appropriate, and recheck with `make status`.
- **Mirror unreachable:** run `npm run health`; verify host listeners `8081` and `8082` separately. The browser uses only `8082` and publisher ingest uses only `8081`.
- **Publisher retry:** run `./ztd public push`, then `npm run health`. A retained outbox retries in order without rerunning evaluation.
- **Malformed projection:** run the contract and feed-transport tests plus `./ztd test unit --pkg ./internal/services/evaluation`. Do not bypass strict validation or publish a known-invalid corpus.
- **No SSE:** verify `/stream` through `npm run health`, reload to reconstruct history, and confirm browser requests do not target `8081`, Gateway ports, report paths, or the provider.
- **Terminal evaluation failure:** retain the report and failed public record, publish the actual exit disposition, reconcile attempt and metric counts, and use a fresh run directory only after fixing an offline defect. Never retry under the same run identity.

Stopping the Gateway (`make down`) stops the in-process mirror and explorer listeners and preserves mirror state and report roots. Gateway cleanup (`./ztd gw clean`) and public-feed reset are destructive operations and require immediate explicit confirmation for their exact scope.
