# Action receipts grew legs

*Field notes, October 24, 2026.*

Some things I have been meaning to clean up are no longer cleanup.

They are becoming critical to ztd being an actual peer-to-peer, zero-trust security layer for AI instead of a large collection of good intentions with a database attached.

The latest example started with one word.

## Action receipts grew legs

ztd has Event Receipts. That is the design: governed requests produce signed evidence as events, and those events live on the event chain at the runtime that executed them.

At some point, an agent started calling them "action receipts."

I noticed. I knew I should correct it. I didn't.

Then the name grew legs.

"Action receipt" stopped being loose wording and became a separate concept in the code. Then it became a separate storage path. Then it became a separate table. I didn't realize how far it had gone until I ran a pile of evaluations, watched the stores get much larger than expected, and started digging into where all the space went.

The same receipt was effectively being represented twice.

v1.3.0 cuts those legs off. Event Receipt is the only receipt now, as originally designed. The separate `receipts` table is gone. Receipt stages live on the `events` chain, and the current receipt is a projection of that signed event history, not a second kind of evidence.

In the eval stores I was investigating, that cut the ztd database roughly in half.

Not through compression. Not through clever indexing. By deleting an accidental architecture that should never have existed.

## Names are load-bearing

This is not the first time a name has steered an agent toward the wrong design.

I called the Events Store the "Local First Audit Architecture." I was trying to emphasize that the executing runtime owns its evidence locally and that the Gateway's copy of a remote Operator's receipts is only a projection.

What the agents heard was: audit system.

So they kept pulling "audit" into names and abstractions until `AuditStore` started becoming a completely different storage path.

That is backwards. ztd has been event-driven from day one. Things happen. Those events are recorded locally. Later, they can be queried for an audit. The audit is a use of the events, not a second universe where the evidence goes to live.

The distinction sounds pedantic right up until one noun turns into another table.

I have the same fight with polling. An agent needs to wait for state to change, sees an HTTP endpoint, and immediately reaches for a loop and a timer. Meanwhile the platform already publishes the event that says exactly when the state changed.

I use VS Code and Cursor now. I've used Windsurf and Devin. I've run agents from all the major providers. Same battle everywhere. If the vocabulary leaves room for two architectures, eventually an agent will build both.

Usually before lunch.

## The ledger is next

Removing the duplicate receipt store explained a lot of the database growth. It did not explain all of it.

The commitment ledger still looks larger than it should, and directory-index changes are the next suspect. I have not proved that yet, so I am not calling it a bug or promising a number. It is simply the next profile.

There is probably room to make that evidence much smaller without weakening what it commits to. That work belongs in v1.3.x or later, after the measurements say where the space is actually going.

I have learned this lesson enough times now: profile first, delete the imaginary explanation, then change the code.

## The evals were grading the wrong contest

Once the tests passed and v1.3.0 looked ready to release, I noticed the eval results were still a little off.

Some scenarios were unfair. The prompt, available tools, or expected evidence put the model in a position where a reasonable answer could fail—or where the model was being asked to recover from a situation the harness had made impossible.

The scoring was off too.

If a small local model uses half the resources of a much larger model and gets 95 percent of its accuracy, the larger model should not automatically win because it collected a few more raw points. For ztd, efficiency is part of capability. A model that is slightly less accurate but cheap enough to run locally, specialize, replicate, and keep inside the trust boundary may be the much better model.

Parameter count cannot just be trivia next to the score. It needs real weight.

That does not mean giving small models a participation trophy. A tiny model that cannot use the tools or follow the contract is still the wrong model. It means the leaderboard should answer the question ztd actually cares about: how much trustworthy work do I get for the resources this model consumes?

The v1.3.0 fairness review found both sides of the problem: failures caused by ambiguous or impossible grading, and passes that were too generous. "Verified" proves that the recorded evidence still matches. It does not prove that the test was fair.

That distinction is now part of the work.

## Tribunal is in consensus

Tribunal is now implemented as the Gateway-hosted L2 consensus provider.

Five enrolled members vote, three approvals form quorum by default, and state-changing actions fail closed when the quorum is not there. The Tribunal can also author a constrained command as one executable plus an argument vector, with no shell-string magic hiding in the middle.

It is real. It is also still a work in progress.

The mechanics exist. The remaining question is whether the member models, prompts, scoring, and evidence produce useful decisions consistently enough to justify the cost. That is exactly what the next evaluations need to answer.

No victory lap yet.

## Less state on disk, different crypto later

The remaining local key and session state that still exists as files is moving toward process memory. Some of that work has already landed for Operator enrollment identity; the rest needs the same treatment. The files were always a temporary bridge, not the destination.

Post-quantum cryptography is coming after that. Not as a sticker, and not by swapping an algorithm name in one package. The protocol, envelopes, identities, signatures, stored evidence, and upgrade story all have to move together or it is just cryptographic cosplay.

For now, v1.3.0 is the release where one accidental noun lost its database.

## Score for the release

- Deleted the receipt system that was never supposed to exist ✅
- Cut the observed eval databases roughly in half ✅
- Confirmed that naming mistakes become architecture ✅
- Found evals that were both too harsh and too generous ✅
- Put Tribunal inside L2 consensus, where it now has to prove itself ✅
- Added "correct agents before they invent a table" to the engineering process ✅
