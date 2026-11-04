# Fictional organizations

Maintained, runnable target infrastructure for learning and demonstrating ztd. Each organization contains synthetic business records, related systems, sensitive data, and repeatable operational scenarios. Integration snippets live in [examples](../examples/README.md); model evaluation fixtures live in [eval](../eval/README.md).

Start with [Zero Trust Paper Company](zero-trust-paper-company/README.md), a small paper distributor with nine Python services sharing SQLite. Its [scenario guide](zero-trust-paper-company/SCENARIOS.md) covers a delivery investigation, purchasing, collections, HR, and retrieval boundaries.

Run the demonstration checks from the repository root:

```bash
make demos-test
```

## Recording a demo video

`make vhs CLIENT=goose PROMPT="I am Priya Shah, the controller. Which customer invoice is past due?"` records the real `ztd mcp agent run` against the Zero Trust Paper Company with [VHS](https://github.com/charmbracelet/vhs) and writes `build/demos/<time>-<client>/demo.mp4`. `goose` runs the smallest local models on purpose (`qwen2.5:0.5b`; `THINK=1` adds a governed-inference scene where `qwen3:0.6b`, the smallest thinking model, shows its signed thinking). Poor answers are part of the demo. `MODEL=<ollama tag>` overrides. The recording does not download models. goose needs `qwen2.5:0.5b` already resident on the Ollama it talks to directly; that tag is not in `eval/model-pull-catalog.json`. `THINK=1` needs `qwen3:0.6b` on the Inference Operator, staged with `ztd eval models pull qwen3:0.6b` when it is missing. See `scripts/demo-record.sh` for prerequisites and `demos/tapes/agent-run.tape` for the scenes.

### Persona scenarios for a release

`demos/vhs-scenarios.json` is the checked-in list of five fixed scenarios, one per Zero Trust Paper Company persona and domain: Priya Shah (finance), Alex Chen (operations, with the thinking scene), Jamie Park (procurement), Sam Rivera (customer support), and Morgan Reed (compliance, who checks that operations cannot read payroll, so the governed call is admitted and the application answers 403). Each entry has an `id`, persona, role, domain, `title` (shown as the first line of the video), `client`, `think`, the exact `prompt`, the `calls` the small model is told about, and `shows`, what a good take shows. Edit a prompt there to tailor a take; nothing else generates them.

- `make vhs SCENARIO=priya-finance` records one scenario (`CLIENT`, `PROMPT` and `THINK` still override the file).
- `make vhs-scenarios` records all five in file order, or `ONLY="priya-finance morgan-compliance"` records some. It writes `build/demos/scenarios-<time>/INDEX.md` (persona, domain, link to each video, attempts, signed tool calls, audit chain head, wall time, prompt, and what a good take shows) and one `<n>-<id>/` directory per take. Watch them, keep the good ones, delete the rest.

Both are local only. No CI or release workflow runs them (they need a local Ollama, VHS, ttyd, ffmpeg and Chrome), and they are not part of `make test`. Expect several minutes per scenario with `qwen2.5:0.5b` on CPU, since a take is retried until it records a signed tool-call receipt.

## Adding an organization

Give each organization a stable directory slug, its own README, fixture sources, explicit initialization/reset commands, ignored local state, and tests. Keep startup offline and dependencies small. Document the fictional business, identities, initial problems, expected effects, and limitations. Use reserved example domains and unmistakably synthetic account references; never copy production records or credentials.

Related systems must share stable record identifiers and preserve business invariants across mutations. Keep money in integer minor units, validate requests, commit related changes atomically, and make mutation retries safe. Seed dates should be relative to an explicit day so collections and scheduling exercises remain useful over time.

Application permissions and ztd governance are separate boundaries. Every organization must describe how its scripts are invoked through a supported ztd ingress, which host owns the target data, and which direct access paths remain outside that ingress. Business logs must never be presented as cryptographic ztd receipts. Keep organization state outside ztd runtime directories.

Add each organization's tests to `make demos-test`, keep them independent of a live Gateway, and update this index and the [documentation catalog](../docs/devs/docs.md#documentation-catalog). Extract shared infrastructure only after another organization needs the same behavior.
