# Shirtless in a field with a reference monitor

*Field notes, November 4, 2026.*

The last field note ended a section with "No victory lap yet."

This is a small one. One lap, at a walking pace, carrying a list of known issues.

ztd is eighteen months of research and development, and this is the first stretch where it behaves the way I pictured it when I started. It isn't finished. It is real, it's working, and it's live: while I wrote this, the public feed at [opendevops.ai](https://opendevops.ai) was publishing signed evidence from a campaign called `v190-formation-07`.

I'm extremely proud of it. I don't usually write that sentence, so I'm leaving it here.

## Not a process, a working thing

Most of what's on offer for putting AI into production is process. Frameworks, checklists, standards, things to try to follow.

I wanted to put forward something you can run.

ztd is my proposal for how AI should get integrated into production: a reference protocol and a platform for zero-trust AI execution. Contracts you can read, a binary you can start, evidence you can verify without taking my word for it. It comes out of decades in the trenches, and the trenches teach one lesson on repeat: whatever can touch production eventually will.

So the model can ask, but it cannot directly act.

## A 1972 idea, pointed at AI

The reference monitor is fifty-four years old. Every access passes through a mediator that is always invoked, tamper resistant, and small enough to verify. Replace subjects and objects with agents and actions, and that's the shape of ztd. A Gateway admits typed intent. An Operator on the target host re-verifies that intent without trusting the Gateway. Only the L5 Actuator executes.

The best thing about a fifty-four-year-old spec is that it tells you when you're lying to yourself. So here's how the last eleven days scored against it.

**Always invoked.** v1.5.0 closed an open door. The generic `/api/v1/data/*` API let app and Operator certificates overwrite signer keys, revocations, users, and settings. Those writes now return 405 for every identity, and certificates get explicit route allowlists. v1.8.0 went further: pulling a model is now a typed, governed mutation. Even the thing that downloads the brain has to ask first.

**Tamper resistant.** Receipts are signed where the work happened, and they chain in a local ledger. Audit checkpoints are signed now too, with an auditor key bound at enrollment. Connected Operators also refresh their consensus trust every 30 seconds, so a removed Tribunal member's signature stops counting. My homepage says "tamper evident," because I would rather undersell that one.

**Small enough to verify.** This is where I don't get full marks. Nobody has formally verified this mediator, including me. What I can offer is the next best thing: the executing Operator re-checks hashes and proofs on its own, evidence verifies offline, the source is public, and the invariants have stable IDs you can audit against.

Two and a half out of three, working, on real machines. I'll take it.

## One binary under 50 MB

Gateway, Operator, CLI, TUI, and eval tooling are all the same static `ztd` binary, 44 to 48 MB depending on the platform.

I keep thinking about what that means at the two ends of the network.

At one end is the dev laptop, which is where a lot of AI agents hold the most ambient authority with the least supervision. Install the ztd plugin for Claude Code, and the ztd tools become the only way the agent can read, change, or run anything. Every other tool gets denied. A laptop picks up a real execution boundary from one small download, with the Gateway and Operator running right there on the machine.

At the other end is the fleet. Operators connect outbound only over mTLS and listen on nothing, so there's no inbound port to scan. Every device keeps its own events, its own keys, and its own evidence. From one place, one person runs one command:

```sh
ztd operator run --all-active --cmd "uptime"
```

In October, 2,000 native Operators enrolled in 54.82 seconds, and that command fanned out to 2,001 targets in 3.89 seconds with zero failures. That's the direction I care about most. Thousands of edge devices, AI doing the legwork, a human at the wheel, and no device trusting anything it didn't verify for itself.

Put the two ends together and I think you're looking at a next-generation, secure, AI-powered DevOps engineer that fits in a binary under 50 MB.

Not bad for a project whose evals once deleted its own repo.

## Invite the provider in

The default deal with AI today is that you ship your data out to get the intelligence in. Logs, configs, tickets, secrets if you're unlucky, all poured into somebody else's context window.

ztd turns that around. The data stays home, and the provider gets invited in, just in time, for the job.

Each runtime owns its local-first events database, vault, and state root. Local stores hold the full content, encrypted. Everything that leaves goes through one egress choke point. Credentials become tokens like `{{UEI_<owner>_N}}`. The model passes the token along without ever knowing the value, and the L5 Actuator resolves it on the host that minted it, right before execution. The capability for that action is minted for that action and then dissolved.

The provider does the job and leaves. The evidence stays. Custody never moved.

And if you'd rather not invite anyone, Ollama and llama.cpp run the same governed path on your own hardware.

To be clear, this is custody, not magic. The scrubber tokenizes the sensitive values it recognizes. If you send a frontier model your source code, it reads your source code. What changes is the default: there's one door, and it's yours.

## Eleven days, ten sets of release notes

Some of my favorites since the last post:

**The judge got audited.** Before v1.5.0, a scored model could grade its own answers, which is about as rigorous as it sounds. Each campaign now pins one judge. Since v1.7.0, every judged grade carries the judge's signed result and receipt, and verification rejects a grade without them. The grader has to show its work like everybody else.

**The platform can tell whose fault it is.** In the v1.5.0 consensus run, Tribunal 3-of-5 with L2 enforced, 52 of 52 cells ran on `nemotron-3-nano:4b`: 28 passed, 22 failed on the model, and 2 hit provider failures. In that release's qualification runs, every non-pass was classified as model or provider, and none as harness or evidence. The one quorum rejection in the run window denied a `run_shell_command` the scenario forbids. 28 of 52 is not a great score for that model. It is a great result for the platform: every failure landed on the model or the provider, and none on my harness or my evidence.

**Formations came back.** Two weeks ago I wrote that they were fucked and that I ripped them out. There are now 82 in the catalog, and every role runs under a typed, versioned persona whose digest is frozen into the evidence. The first version was fucked. The idea wasn't.

**The file ledger got one write path.** Every file mutation goes through it now, snapshots live in a sharded tree, and file history costs what the file's history costs, not what the session costs. v1.9.0 also stopped a large rewrite's diff from stalling ledger writes across every session on the runtime.

**You can watch it.** The Live view shows Consensus votes, Deterministic grades, and Judge verdicts in each assignment's row as they happen. There's a nerdy joy in watching a tiny model get graded in real time by something that has to sign its opinion.

In October I wrote that if the data said ztd doesn't help, I would stop. That's still the deal. I don't have the final answer yet. What I have now is a harness whose answer I would believe.

## Not there yet

Getting very close is not the same as there.

- **Scale:** Those 2,000 Operators were processes on one machine. Thousands of real devices on bad links is a different test, and I haven't run it.
- **The verdict:** The evals can now produce a trustworthy answer on whether all this makes agents measurably safer. They haven't finished answering.
- **The boundary:** The plugin stops Claude Code's own tools, not other processes on the machine, its hook needs a POSIX `sh`, and if the hook can't run, Claude Code lets the call through. Agents that can't disable their native tools are only partly governed.
- **Crypto:** Post-quantum is still ahead of me, and it still has to move the protocol, identities, and stored evidence together.
- **Headcount:** The release process doc is a single-maintainer checklist, because there's a single maintainer.

Which brings me to the field.

## Join me

I'm the shirtless dude dancing in the open field at the music festival.

I know how it looks. One guy, a fifty-four-year-old security idea, and a lot of confidence. What I'm waiting for is my green shirt dude, the one who jumps up and gets this zero-trust, local-first revolution fired off.

Maybe that's you. The dance floor is one command:

```sh
curl -fsSL https://zerotrustdata.io/start.sh | sh
```

On Windows:

```powershell
irm https://zerotrustdata.io/start.ps1 | iex
```

Or put your Claude Code on the governed path:

```text
/plugin marketplace add zerotrustdata/ztd
/plugin install ztd@ztd
```

Watch it run at [opendevops.ai](https://opendevops.ai). Read the source at [github.com/zerotrustdata/ztd](https://github.com/zerotrustdata/ztd) (BSL 1.1 today, Apache 2.0 on the change date). Then try to make it do something it shouldn't, and [tell me](/#contact) what you find.

I've spent a long time building a thing that doesn't take anyone's word for anything. So don't take mine.

## Score for the eighteen months

- Spent a year and a half on a 1972 idea ✅
- Fit it in one binary under 50 MB ✅
- Fanned out to 2,001 targets in 3.89 seconds ✅
- Stopped models from grading their own homework ✅
- Made the model download ask permission ✅
- Taught the process manager to keep its hands off PID 1 ✅
- Took my shirt off in public ✅

Green shirt: still looking. I'll keep dancing.
