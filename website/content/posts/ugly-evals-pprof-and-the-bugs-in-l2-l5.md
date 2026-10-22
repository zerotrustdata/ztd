# Ugly evals, pprof, and the bugs in L2–L5

*Field notes, October 20, 2026.*

Anthropic gave me a $1,000 grant to build something with Claude. I decided the most useful and honest thing I could do with it was to turn Claude Code loose on ztd: review the code, challenge my assumptions, and help me push our evaluations into real production territory.

Now those evals are finally starting to come together. There is still an enormous amount of work to do, but they are doing exactly what I hoped they would: pointing out major flaws in the code.

Even when the evals themselves are flawed.

## Bolted-on is fine to start

When I started writing evaluations, they were ugly. They were bolted onto the side of the platform with whatever glue worked at 2 a.m. But I was fine with bolted-on, because my rule from day one was simple: as long as they exercise real production code, an inelegant test that breaks the system is infinitely more valuable than an elegant test that passes on a simulation.

Over time, I've been pulling those evaluations out of the scratch heap and integrating them natively into the protocol and the runtime harness. The fact that I am finally slamming into severe performance walls and architecture gaps doesn't mean the harness failed; it means it is finally doing its job.

When an evaluation stresses the system hard enough to expose architectural cracks, you don't pretty up the test. You fix the architecture.

## Formations were fucked, so I ripped them out

Not every experiment worked out. "Formation evaluations" were my first attempt at building a real multi-agent chat flow test—running multiple models in coordinated roles against real scenario workspaces.

They were fucked.

The abstractions leaked, the context handoffs were brittle, and instead of isolating model behavior, they mostly tested how well my harness could untangle its own confusion. When something is fundamentally wrong, you don't polish it. You rip it out. So I killed formation evaluations and refocused the pipeline on clean, deterministic, production-bound execution.

## The cracks in L2–L5

Once the evals started hitting the actual production codepaths without mercy, things broke. Not in mock layers—in the actual governance pipeline.

I found bugs across L2 through L5:
- **L2 Consensus:** Quorum verification and signer binding had subtle timing gaps under heavy evaluation fan-out.
- **L3 Notary:** Cryptographic proof validation and passkey approval handoffs weren't failing closed cleanly when sessions were interrupted.
- **L4 Warden:** State-root recalculation and nonce reservation hit race conditions under rapid, repeated scenario dispatch.
- **L5 Actuator:** Action handler errors were occasionally getting swallowed instead of propagating back as authoritative, signed `FAILED` terminal receipts on the local commitment ledger.

To track down the memory churn and lock contention under this load, I wired loopback-only `net/http/pprof` directly into the Gateway (`ztd gw start --pprof-listen 127.0.0.1:6060`). I've spent days staring at heap allocations, goroutine dumps, and mutex blocking profiles, hunting down the bottlenecks.

If nothing else, these evals have dragged every performance kink and lazy assumption into the light. Releases v1.1.2 through the upcoming v1.2.0 are addressing those issues head-on: fixing failure receipt integrity, cleaning up posture enforcement codepaths, and making sure the five-layer pipeline actually holds under fire.

## Burning the last tokens

I'm down to the tail end of that Anthropic grant. I've decided to spend the last of my tokens on getting the full evaluation pipeline completely cleaned up, properly using the production code.

This isn't just about clean engineering. It's the best way I have to quantify if ztd actually helps at all.

I need unambiguous data: does a zero-trust, five-layer governance pipeline make AI agents measurably safer and more reliable, or are the trade-offs simply not worth it? If I find the data says it doesn't help—or that the overhead isn't justified—I will stop work on ztd. I have no interest in building security theater.

Otherwise, I'm going to keep pushing for zero-trust, data-sovereign AI. Specifically, in support of local, open-source models that can run on consumer hardware.

I believe that the right system of small, specialized models—operating locally with strict boundary controls, signed receipts, and verifiable memory—can be more accurate, secure, auditable, and flexible than a monolithic cloud API. More importantly, a system like that can actually learn and adapt over time without shipping your private data off to someone else's datacenter. That is what I hope to make available for everyday users.

## Moving on

A quick update on the interview process from the earlier field notes: no word yet on the role that prompted me to perform the 2,000-Operator scale test. At this point, it looks like they decided to go in a different direction.

That happens. The scale tests were worth running anyway—they proved the Gateway can handle 2,000 live native Operators with sub-four-second fan-out, and the profiling work from this week makes that core even leaner.

I'm continuing to build, research, and consult. If you are looking for someone to help you build, fix, research, deploy, design, or audit AI systems, please reach out to me at [danny@lateraluslabs.com](mailto:danny@lateraluslabs.com).

## Score for the week

- Burned an Anthropic grant breaking production code ✅
- Ripped out broken formation evaluations ✅
- Added `pprof` and profiled Gateway lock contention under load ✅
- Fixed bugs in L2, L3, L4, and L5 across v1.1.2–v1.2.0 ✅
- Put the core thesis on trial: let the evals prove if ztd is worth building ✅
