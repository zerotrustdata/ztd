# Fictional organizations

Maintained, runnable target infrastructure for learning and demonstrating ztd. Each organization contains synthetic business records, related systems, sensitive data, and repeatable operational scenarios. Integration snippets live in [examples](../examples/README.md); model evaluation fixtures live in [eval](../eval/README.md).

Start with [Zero Trust Paper Company](zero-trust-paper-company/README.md), a small paper distributor with nine Python services sharing SQLite. Its [scenario guide](zero-trust-paper-company/SCENARIOS.md) covers a delivery investigation, purchasing, collections, HR, and retrieval boundaries.

Run the demonstration checks from the repository root:

```bash
make demos-test
```

## Adding an organization

Give each organization a stable directory slug, its own README, fixture sources, explicit initialization/reset commands, ignored local state, and tests. Keep startup offline and dependencies small. Document the fictional business, identities, initial problems, expected effects, and limitations. Use reserved example domains and unmistakably synthetic account references; never copy production records or credentials.

Related systems must share stable record identifiers and preserve business invariants across mutations. Keep money in integer minor units, validate requests, commit related changes atomically, and make mutation retries safe. Seed dates should be relative to an explicit day so collections and scheduling exercises remain useful over time.

Application permissions and ztd governance are separate boundaries. Every organization must describe how its scripts are invoked through a supported ztd ingress, which host owns the target data, and which direct access paths remain outside that ingress. Business logs must never be presented as cryptographic ztd receipts. Keep organization state outside ztd runtime directories.

Add each organization's tests to `make demos-test`, keep them independent of a live Gateway, and update this index and the [documentation catalog](../docs/devs/docs.md#documentation-catalog). Extract shared infrastructure only after another organization needs the same behavior.
