# From 936 to 2,000 Operators on one Gateway

*Field notes, October 8–11, 2026.*

The first run stopped at 936. I'd pointed a thousand Operators at one ztd Gateway, and by the time I gave up, the
stragglers were stuck resuming the same dead enrollment request every time they restarted, and the Gateway had
bounced 1,028 requests off a quota that was full of abandoned ones.

That run started with a question from a senior infrastructure engineer. It was the boring, correct kind of
question: what does one Gateway actually do when a thousand Operators enroll against it, and what does the fleet
look like afterward? I had run tens of Operators. I had never run a thousand. I didn't have data, so I went to get
some.

Four days later, 2,000 native Operator processes on the same box enrolled in 54.82 seconds, sat through a
five-minute soak, and answered a governed fan-out to 2,001 targets in 3.89 seconds with zero failures. (The extra
target is the Gateway's embedded Operator.) We bounced the Gateway, all 2,000 came back without rotating sessions,
and the next fan-out hit every target too.

Getting there touched storage, authentication, deployment, credential delivery, and the enrollment state machine.
Twice, a fix just moved the wall somewhere else. I learned that a certificate getting issued is a lousy definition
of "enrolled." And I spent a good while ripping out concurrency gates, swore them off, and then ended up adding
approval pacing anyway, just tied to the moment a worker can actually take a command.

2,000 is where I stopped on this host. It's a point I tested, not a ceiling I measured.

## The shape of the thing

ztd (Zero Trust Data) is a zero-trust execution platform. A Gateway admits typed intent. An Operator, a process on the target host,
re-verifies that intent on its own and executes it. Operators connect outbound only, over mTLS, and listen on
nothing. Governed mutations ride in signed envelopes through five layers: doctrine, consensus, notary, warden,
actuator. Every run here used doctrine posture, so doctrine, warden and actuator are enforced, while consensus and
human ratification aren't gating each dispatched command. The actuator signs event receipts and appends them to a
commitment ledger on whichever host did the work.

Enrolling an Operator is one of those mutations. Two, really. The Operator generates a key pair and files a
request. An owner approves it. The Operator completes enrollment, and the Gateway issues its certificate and creates
its session, each as a governed envelope with its own event receipts and state-root reads. When I started, one completion
cost about 25 write transactions across the Gateway and audit SQLite databases. That's nothing once. A thousand of
them landing in the same few seconds is a different animal.

## The first attempt

Empty machine, empty Gateway: 12 CPUs, 32 GiB, one Gateway process, one native process per Operator. I drove the
fleet with nothing but `ztd operator` commands (`deploy --local --background --approve`, `list`,
`run --all-active`, `stop`). No harness, no wrapper scripts, no abort gates. Heartbeats at the 30 s default, TLS
verification on, and samplers recording Gateway and fleet resource use every 10 seconds.

The first thing I hit was that the CLI wouldn't deploy more than four at a time. One constant,
`PlatformEnrollmentMaxLiveOperatorRequests = 4`, was doing double duty as the CLI's `--parallel` ceiling and the
Gateway's live-request quota for enrollment. I bumped it to 100 on both sides and kept going.

| Step | Result |
| --- | --- |
| N=1 | Enrolled in 0.7 s |
| N=100, one call, `--parallel 100` | 23.1 s, no failures. `run --all-active` over 101 targets (the embedded Operator counts): 101/101 succeeded, p50 226 ms, p99 375 ms |
| 900 more, `--parallel 100` | 10 min 14 s. 289 of 900 failed after three attempts each. First failure at op-00531 |
| Retry of the 289, 25 at a time | 224 recovered, 65 failed again |
| One stuck worker: `reset-identity`, redeploy | Worked, 2.5 s. I stopped before doing the other 64 |

936 of 1,000 enrolled and connected when I called it.

The weird part is that the idle fleet was fine. With 936 connected for over five hours, Gateway RSS sat near 976 MB
with 2,862 file descriptors, 20 threads and 2,808 connections on the mTLS port (three per Operator), and none of it
trended. Each Operator process cost about 19 MB PSS. With a small fleet I'd measured 11 to 12 MB, and I never
worked out why it went up. It was flat for hours, so I don't read it as a leak, but I wouldn't quote the smaller
number either. Memory wasn't what stopped us at a thousand. Enrollment was.

## Reading the failure

The logs described a feedback loop. They didn't say what kicked it off.

With a few hundred Operators already heartbeating, the enrollment path slowed down under 100-way enrollment.
Clients hit their 30 s enrollment timeout and hung up, and the Gateway logged `context canceled` and `broken pipe`
hundreds of times. An abandoned request stays non-terminal until its 30-minute TTL, and it keeps counting against
the live-request quota, which is how we got those 1,028 rejections. Meanwhile the CLI retried every failure up to
three times, piling more requests onto a quota that was already full of dead ones. Lovely.

Two more defects fell out of the same run. A worker that fails mid-enrollment persists its pending request and
resumes that same dead request on every restart, so redeploying the directory doesn't help. Only `reset-identity`
got one unstuck. And `operator stop` with no arguments stopped workers one at a time, each through a governed
request with a timeout and a TERM fallback. It managed about 215 of 936 in ten minutes. I finished the job with
TERM.

What I didn't have was a reason for the slowdown, because I hadn't taken a profile. Everything after this is me
trying to get one and then doing something about it.

## The wrong first fix

My first move was the reflex: bound the work. I put concurrency gates in front of enrollment intake and issuance,
with queue-wait limits and counters to report on them. The first sweep ran with the race detector on, which is
roughly 7x slower, so I threw it out and redid it on a normal build.

The gates did nothing. Throughput was flat at 46 to 58 completions/s with limits of 1 to 4, and worse above that,
because the writer behind them was already serial. We'd built a nice orderly line in front of a single cashier.

I tore out the gates and the counters and wrote myself a rule for the next pass: cut the work per enrollment before
building machinery around it. Gates only limited entry to a writer that was already slow; they didn't take any work
off it. The 2,000-Operator run would later make me eat part of that rule, but we'll get there. The other rule held
up fine. The integration tier in this repo always adds `-race`, so race runs check correctness, and throughput
numbers come from a normal build.

## Costs that grew with history

L5 reads the Gateway's state root around every governed mutation, about three times per envelope. That root was a
hash over a scan of every document, recomputed under one global mutex any time a global version counter moved.
Let's see what one write followed by a root read costs at 100, 1,000 and 10,000 event_receipt-sized documents:

| History rows | Full-scan root | Incremental root |
| --- | --- | --- |
| 100 | 7.9 ms | 5.1 ms |
| 1,000 | 33.2 ms | 5.4 ms |
| 10,000 | 317 ms | 5.6 ms |

Both columns come from the same race-enabled harness, so ignore the absolute values and look at the shape. The old
cost is linear in history. Every event receipt we'd ever written made the next one more expensive.

The replacement is a persisted incremental Merkle commitment. Leaves are SHA-256 identities over (source, key), with
length-prefixed fields and a domain tag per hash type. They land in 65,536 buckets under a fixed 16-ary tree four
levels deep, and only non-empty nodes get stored. The bound and observed tiers have separate trees. Triggers on the
document, KV and blob tables mark changed keys inside the writer's own transaction, which catches every writer,
direct SQL and TTL maintenance included. A root read with nothing dirty is one SELECT and takes no Go mutex.
Otherwise one `BEGIN IMMEDIATE` rehashes only the dirty leaves, their bucket, and four levels of ancestors.

This is the kind of code that's easy to get subtly wrong, so the tests are mean about it. They compare against an
independent full-rebuild oracle over a 300-step random sequence of inserts, updates, deletes and tier moves. They
SIGKILL a child process after four committed writes, and again in the middle of a flush, then check the reopened
tree against the pre-crash root. They run four writers and four readers, and every root a reader sees has to be the
oracle root of some committed prefix and can never move backward. Then I broke the tests' own harness two ways
(committing after the flush, skipping deletes in the replay) to make sure they'd actually fail. They did.

One semantic change came along for the ride: expired but unswept TTL rows now stay committed until maintenance
deletes them, so wall-clock time no longer changes the root between two readers.

The second history-dependent cost was a cache. Every document write fired a trigger that deleted the document's
cache key and every query-cache key for the collection, using a GLOB over a computed pattern. SQLite can't serve
that from the primary-key index, so every document write scanned the KV table. In a 2,000-insert profile (race
build again, relative numbers only), an insert cost 2.1 ms with all triggers, 1.35 ms without that one, and 0.68 ms
with none.

Before touching it, I went looking for what the cache was actually for. It stored copies of SQLite rows... in the
same SQLite database. The Python application reached it through the same mTLS hop as a direct document read, so a
hit saved nothing over a primary-key read, and its reads were off by default anyway. The Go side wrote it once, at
bootstrap, and nothing ever read it. The triggers existed because Go services write those same documents directly,
so correctness hung entirely on them.

So I deleted the whole thing: the triggers, the `/api/v1/kv/` HTTP surface, and on the Python side a three-layer
wrapper (cache-aside service over DB service over client), replaced by one document service. 99 files, 698 lines
added, 3,901 removed. If profiling ever justifies caching a hot read, it'll live in process at the service that owns
the read, not in a table sitting next to the data it copies.

## Stage the cohort, approve once

Deployment held a shared mutex around each worker's approval request, so approval was serial by construction, and
`auth enroll approve --all` was a loop of single POSTs. I replaced both. At this stage, deployment staged the whole
cohort, then one typed request approved a fixed snapshot of request IDs and displayed fingerprints as a single
governed action. The executing side rechecks authority, pending state, expiry and fingerprints in one transaction,
and one bad member rolls back the whole cohort. Every member records the same event receipt and envelope ID. A batch
decision takes about 25 ms for 100 requests and about 240 ms for 1,000.

Two supporting changes went in with it. Pending capacity became 2,048, decoupled from the then-current CLI
parallelism cap of 100, and admission (dedup, quota count, insert) now runs under one `BEGIN IMMEDIATE` so two
creates can't both grab the last slot. And deployment stopped finding request IDs, sessions and readiness by
parsing log lines. Yes, it was parsing log lines. I reproduced that failure first, with a test where a valid pending
request and no log output makes deployment time out. The worker now writes a small typed progress record through
the runtime file service, tagged with a per-deployment launch UUID, so a stale record from an earlier process gets
rejected without comparing clocks.

## A fixture, and where the time actually went

The service fixture releases N goroutines together to create requests, approves the cohort with one batch decision,
then releases N synchronized completions. Every call runs under a context that mirrors the client's HTTP timeout at
the time, and the report gives p50, p95, p99 and max per phase. It runs in process, so HTTP, subscription readiness
and heartbeat traffic aren't in it.

With the gates gone, I folded the per-completion work: event receipt and ledger commitment in one audit transaction, final
event receipt and persistence attestation in one write, nonce reservation as a single INSERT keyed on the primary key. By
code count that's about 25 write transactions down to about 19. Then N=100 ran at 13 completions/s. Creation ran at
15 to 21/s, where the gated runs had done over 100.

So we removed work and it got slower. Great.

The CPU profile held 2.24 s of samples across 12.5 s of wall time. The process was mostly sitting around waiting.
Statement preparation was 23% of the samples and fsync was 14%. The fold hadn't really mattered, and the gates had
been hiding something.

It was in the SQLite open path. `sqliteutil.OpenDB` passed `_synchronous`, `_journal_mode` and `_busy_timeout` as
DSN parameters, which the pure-Go driver doesn't recognize. It then ran the PRAGMAs through `sql.DB.Exec`, which
applies them to whichever single pooled connection happens to serve that call. Every other connection in the pool
kept synchronous=FULL, a busy timeout of zero, and foreign keys off. The read-only opener had the same bug with
different parameters.

Oh, fuck.

I switched both to the driver's `_pragma` parameter, so every physical connection gets configured when it opens,
and made a failed pragma fail the open instead of getting logged and ignored. The regression test holds three
distinct physical connections and checks each one's settings. On the old code it fails with busy timeout 0.

That one change took N=100 from 13 to 99–109 completions/s. I didn't trace individual busy retries, so the
mechanism is inference: connections with no busy timeout fail on a lock and fall into application-level backoff.
The 8x jump from a single fix is the evidence I've got.

N=1,000 still took 11.0 to 11.7 s, and the slowest completions were blowing the client's HTTP timeout. The profile
put 45% of samples in `supersedeOperatorLeases` and 38% in the whole-fleet heartbeat reconciliation underneath it.
When an Operator completes enrollment, the Gateway retires earlier leases for the same owner and fingerprint. It
found them with a generic document query that reconciles the liveness of every active Operator and decodes every
runtime configuration along the way. To find a lease or two. The enrollment path now uses an indexed owner and
fingerprint lookup (a partial expression index on the existing documents table) that does no writes. Reconciliation
stays on the registry and liveness reads, where it belongs.

Three unprofiled runs per size on that build:

| N | Create wall | Batch decision | Complete wall | Complete rate | p50 | p99 | max |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 100 | 0.41–0.55 s | 24–29 ms | 0.90–0.91 s | 110–111/s | 0.73–0.76 s | 0.88–0.90 s | 0.90–0.91 s |
| 1,000 | 3.7–4.1 s | 238–241 ms | 8.4–9.3 s | 108–120/s | 6.9–7.7 s | 8.3–9.2 s | 8.4–9.3 s |

What I wanted was for throughput to stop depending on N, and it did: about 110/s at 100, 108 to 120/s at 1,000.
Before, it sagged as the fleet and history grew. A synchronized burst of 1,000 against a serial writer still queues,
which is why p50 sits near 7 s and the slowest completion lands at 8.4 to 9.3 s.

## The live run

Fixture numbers are nice. I took that build to a real fleet: N=1, then 99 more, then 900 more, through the same
`operator deploy` command, with approval as one batch after each cohort staged.

- N=1: deployment 0.71 s, governed fan-out 2/2 including the embedded Operator, 10 ms.
- 99 more, to N=100: deployment 3.53 s. Fan-out 101/101 at concurrency 64, 652 ms wall, p50 293 ms, p99 613 ms. The
  first attempt took 23.1 s to enroll its first 100 from empty, but the cohorts differ (this one was incremental),
  so I'm not quoting a ratio.
- 900 more, to 1,000: failed after 26.4 s. 595 workers logged an enrollment failure when their completion request
  hit the client's HTTP timeout. 405 survived.

The fixture said 8.8 s for a thousand. The real fleet said 595 failures. The gap was authentication. A worker that
has just finished enrolling connects and authenticates its session while everybody else is still enrolling, and
`ValidateOperatorSession` read the operators collection through, you guessed it, the same generic query behind the
lease problem: reconcile the whole active fleet, decode every runtime configuration, maybe write some staleness
transitions. The fixture had never authenticated anybody, so it never saw this.

I reproduced it first. A test that validates one session fails, because validating it changes the state root, and a
read has no business doing that. Then I extended the burst fixture to validate a session right after each
completion. Before the fix, N=1,000 took 18.0 s (p99 17.8 s), and the CPU profile put 59% in session validation and
52% in fleet reconciliation. After the fix, a lookup bounded to two records by a partial expression index on session
identity, it took 9.3 s with the profiler on and 8.8 s without. Total CPU samples dropped from 65 s to 19.5 s. The
bound is two on purpose, so duplicate and terminated identities still reach the auth code, which rejects them.

The lease lookup and the session lookup were the same bug wearing different hats: an identity read going through a
path built for liveness reads, with a fleet-wide scan and writes as side effects. Now, for anything on the
enrollment path, I ask two questions. Does this read write? Does it touch rows other than the one it asked for?

The live run also taught me something about counting. Right after the failed cohort, the Gateway's registry listed
1,000 remote active identities plus the embedded one, even though 595 workers had failed. Issuance is detached from
the HTTP request on purpose (a client disconnect used to fail the ISSUE envelope and roll back the lease), so it
kept completing after the clients gave up, and a fresh registry row counts as active until the liveness window runs
out. Issued identities are not a ready fleet. The CLI decides success by whether a worker can actually receive a
command, and that's staying the criterion.

Then I reran N=1,000 with the auth fix, and partway through, the root volume (97 GiB) filled to zero bytes. SQLite
writes failed, worker logs got truncated, completion returned 500 while L5 was persisting event receipts, and deployment
failed waiting for readiness. Retained fleet directories from earlier runs had eaten about 13 GiB, and in an earlier
live run the Gateway database and WAL had peaked around 125 MB and 840 MB. With truncated logs I can't derive a
success count, so that run is garbage and I'm not citing anything from it.

## Polling, and the run that passed

The next live run, on a build with the SQLite and lookup fixes, got 991 of 1,000 ready in 29.3 s and exited 1. Nine
workers failed when their completion request hit the client's HTTP timeout. Close. But the timing was the
interesting part.

Time from the approval commit to a worker holding its credentials had a p50 of 10.7 s and a max of 17.6 s, and it
was ordered by how long each worker had *already waited before approval*, not by the Gateway's queue. Workers that
had waited 0 to 2 s got credentials in about 4 s. Workers that had waited 8 to 10 s needed about 16 s. That isn't a
server queueing. That's a client sleeping.

Sure enough. The enrollment client polled `GET /status` with a doubling backoff from 500 ms to 30 s, so a worker
found out it was approved up to one backoff step late. It also retried the Gateway's held-lease 429 with the same
backoff, and looped on a bootstrap 403 at submit. This platform is supposed to be event driven, and the Gateway
already publishes an approvals-changed event the moment a decision commits. I'd spent three sessions bolting
machinery onto this path (retry helpers, typed contention errors, a detached context, driver wait bounds), and none
of it touched what the timings were pointing at. So I stopped, wrote the review questions down, and only then went
back to the code.

The next version pulled the polling out and used the event system that was already there:

- Approval polling is gone. A worker makes one `GET …/status?wait=true`, which the Gateway holds on the existing
  pub/sub approvals event and answers when the decision commits. The request's own expiry bounds it.
- The submit retry loop on bootstrap 403 and the held-lease 429 retry are gone.
- Deploy staging and readiness no longer poll a progress file. The worker sends its per-deployment launch ID when it
  creates the request and when it subscribes to its command channel, and the Gateway announces both on the existing
  event streams, to the owner's CLI session as well as web. No new event type, stream or endpoint. Readiness now
  means the command channel was acknowledged, which is what the CLI actually cares about, not that an identity was
  issued.

The first run without polling went the wrong way: 328 of 1,000 ready, exit 1, in 22.4 s.

What the hell. The Gateway had issued and completed all 1,000 by 11:09:50, thirteen seconds after the approval
commit. But with polling gone, all 1,000 workers woke at the same instant and posted their completions together,
and 672 clients gave up at the client's HTTP timeout. Those abandoned requests account for every one of the 1,344
`Failed to encode JSON response` (broken pipe) and `heartbeat stale` log lines, each emitted twice per event. That
also explained the 18 encode errors from the run before: responses written to clients that had already left.

That client timeout was a 10 s constant with no requirement behind it, and the Gateway's 30 s server write timeout
already bounds the call. I removed the client timeout on submission and completion and ran it again.

N=1,000, one `operator deploy --count 1000 --background --approve` call from an empty Gateway, same 12-CPU, 32 GiB
host:

| Measure | Result |
| --- | --- |
| Deploy wall time | 28.63 s, exit 0 |
| Workers ready (command channel acknowledged) | 1,000 of 1,000 |
| Requests created | 11:15:21 to 11:15:31 |
| Credentials issued | 11:15:33 to 11:15:39 |
| Enrollments completed | 11:15:35 to 11:15:49, about 14 s after approval, steady throughput |
| Gateway ERROR / WARN lines | 0 / 0 |

First clean thousand. No failures, no retries. Still only one enrollment run from a fresh Gateway, though, with the
soak, repeat runs and restart test still ahead. And this HTTP enrollment path turned out to be an intermediate
version; the 2,000-Operator work below replaced it.

### What the fleet does

With all 1,001 Operators (1,000 workers plus the embedded one) registered active and 0 stale, I ran
`operator run --all-active --cmd true` at three concurrency levels, three rounds each. Every round succeeded on all
1,001 targets. Percentiles are per round, not averaged.

| Concurrency | Wall (ms) | p50 (ms) | p95 (ms) | p99 (ms) | Max (ms) |
| --- | --- | --- | --- | --- | --- |
| 16 | 2,153 / 1,083 / 2,047 | 21 / 12 / 9 | 78 / 26 / 21 | 224 / 178 / 171 | 771 / 192 / 1,807 |
| 64 | 985 / 1,058 / 1,820 | 39 / 39 / 54 | 68 / 81 / 119 | 201 / 219 / 253 | 984 / 1,058 / 1,820 |
| 1,001 | 1,328 / 1,366 / 1,359 | 545 / 573 / 534 | 859 / 879 / 895 | 885 / 890 / 910 | 1,326 / 1,363 / 1,357 |

Each cell lists rounds 1, 2 and 3. At N=100 the same command over 101 targets took 142 to 219 ms wall across nine
rounds, so ten times the fleet costs roughly five to ten times the wall time for the same work. Firing at all 1,001
targets at once finishes in about 1.35 s with a p50 near 550 ms, because every dispatch is in flight together. The
occasional 1.8 to 2.0 s wall at concurrency 16 and 64 is one slow dispatch, not a general slowdown (the max equals
the wall in those rounds). I didn't chase it.

Resource use right after the fan-out, with 1,001 Operators connected:

| Measure | Value |
| --- | --- |
| Registry | 1,001 active, 0 stale |
| Gateway RSS / threads | 1.08 GB / 54 |
| Gateway file descriptors | 4,078 |
| Established connections on :8443 | 4,000 (about four per Operator) |
| Fleet PSS | 14.2 GiB, about 14.5 MB per Operator |
| Host | 1-minute load 13.7 just after the burst, 5 GiB available |

One snapshot, not a trend. The per-Operator figure lands between the 11 to 12 MB I saw with a small fleet and the
19 MB from the first attempt, and I still don't know why it moves. Connections per Operator are four here against
three in the first attempt. Haven't looked into that either.

## Stopping a fleet

Serial stop became a pool of at most 64 workers, reusing the worker-pool pattern from `operator run`, with
per-target results in discovery order. After the disk incident, 335 workers stopped in 12.5 s. All 335 needed TERM.

That was the second problem, and it wasn't new. In the earlier live run, all 405 Operators had acknowledged a
governed stop and *still* needed TERM before the grace period ran out.

There were two causes. The top-level `RunOperator` waited on service errors or OS signals but never watched the
service's own external-shutdown cancellation, so an acknowledged shutdown never turned into a process exit. The
Operator agreed to shut down and then just kept running. `ZtdoService` now exposes a `Done()` signal and
`RunOperator` observes it. A live single-Operator stop with `--reason` then came back `method: exited` in 30 ms,
with `EXECUTING` and `COMPLETED` SHUTDOWN event receipts in the Operator's log.

The second cause was that a stop *without* a reason never reached execution at all. A `ShutdownRequested` message
with every field at its default marshals to zero bytes. Two checks treated zero bytes as a missing payload. L4 only
exempted heartbeats, so it rejected the envelope and recorded a rejected-transaction event receipt. Behind that sat a
duplicate check in the pub/sub handler that dropped the envelope with no event receipt, after nonce reservation. The CLI
waited out its 5 s API timeout and fell back to TERM. The browser's stop route sends exactly that empty message, so
this was the common case, not an edge. One function now owns the zero-byte exemption (heartbeat and shutdown), the
duplicate check is gone, and a stop with no reason returned `exited` in 94 ms.

## Making the experiment repeatable

Up to here, every run was driven by individual CLI commands. Great for diagnosis, miserable for repeating a full
qualification, which really means owning the Gateway, the fleet, the evidence and the cleanup as one operation. So
that lifecycle moved into `ztd test scale`, with `operator deploy` as its fleet orchestrator. The test command calls
the same deployment code used outside the test. There's no second implementation of launching and tracking workers
hiding in the test tree.

That meant cleaning up deployment itself. Local targets call a typed Go host agent in process. Remote targets use the
same agent through a shared native SSH client and SFTP. The binary is copied once per host, installed through a
staging rename, and hard-linked into worker directories. Typed requests carry the host operations. The old generated
shell commands and system `ssh`/`scp` paths are gone.

Deployment and streaming have different lifetimes. A deployed worker runs detached and connects straight to the
Gateway. A streamed worker lives for the SSH session and sends its Gateway traffic through SSH forwards. Both are
implemented and have integration coverage; remote hardware qualification is still ahead of this local experiment.

The harness starts an isolated doctrine Gateway on free ports, enrolls an owner, deploys the cohort, runs the fleet
scenarios, samples resources, and stops only this run's processes. On headless Linux the very first attempt died
before the fleet even started, because there was no keychain. Classic. The harness now provisions a per-run master
key in a private temporary directory outside the run root and removes it at teardown.

Cleanup became part of qualification too. The host agent stops workers concurrently, scoped to the deployment
directory, and on Linux the process-exit waits use pidfds. That's process teardown, separate from the governed
SHUTDOWN command above. A fast harness teardown tells us nothing about governed bulk-stop throughput.

The whole qualification is now one automated command, `make test-scale` (`ztd test scale` underneath), and it is
deliberately not part of the ztd pipeline. `make test` and CI never run it. A real fleet takes minutes of soak and
thousands of processes, which a shared runner can't host and a pull request shouldn't wait on. CI keeps the cheap
regression coverage instead: the in-process enrollment-burst fixture and a three-Operator deployment lifecycle. The
scale run is something you invoke on purpose, on a box you've set aside for it. The latest one, on October 12 at the
default count of 100, passed every phase: Gateway up in 0.6 s, enrollment in 1.4 s, steady state, soak and fan-out
in 346.4 s, Gateway restart recovery in 15.1 s, and teardown in 0.6 s with all 100 workers stopped.

## A thousand, including the restart

Two full local runs at N=1,000 passed on October 10. The second used the rebuilt binary and the automatic
headless-key setup:

| Phase | Result |
| --- | --- |
| Enrollment, one staged cohort and one approval batch | 21.2 s |
| Five-minute idle soak | 1,000/1,000 active, 0 stale |
| Fan-out to 1,001 targets in one wave | 1.36 s wall; p50 717 ms, p95 926 ms, p99 945 ms; 0 failures |
| Gateway process restart | 1.1 s |
| Full fleet heartbeat recovery after restart | 32.0 s; 0 sessions rotated |
| Fan-out after recovery | 1,001/1,001 succeeded; p95 712 ms |
| Harness teardown | 2.8 s; 1,000 workers stopped |

At the final resource sample, Gateway RSS was 716 MB with 24 threads and 3,054 file descriptors. The Gateway
database was 144 MB and its WAL 1.5 MB. Fleet PSS was about 12.5 GB total, or 12.5 MB per Operator, with 12,619
threads across the workers. Those are the units the qualification plan recorded, and it's a snapshot after recovery,
not a peak-memory measurement.

That closed the biggest holes: the thousand was repeated, the fleet survived a soak, and bouncing the Gateway didn't
force anybody to re-enroll. Those runs still used the old transport, though, and doubling the fleet found what was
left wrong with it.

## Two thousand: committed doesn't mean delivered

The first N=2,000 attempt got 1,666 Operators enrolled and heartbeating. The other 334 exited with an EOF from the
completion POST. The harness sat waiting for workers that were never going to become ready, and I killed it before
any soak or fan-out ran.

Here's the annoying part. The Gateway had logged completed enrollment for those exact request IDs, then failed to
write the response with an I/O timeout. Removing the client's short timeout had just moved the failure to the
Gateway's 30-second HTTP write deadline. The mutation could commit after the response window had already closed.

One trace made it clear I couldn't treat handler completion as request completion. The CREATE_SESSION handler
returned, but its COMPLETED event receipt was recorded 18.5 seconds later. The response write failed in that same
millisecond. I didn't finish attributing that interval to database waits, signing and scheduling, so I'm not going
to pretend I have a precise breakdown.

Memory wasn't it this time. Gateway RSS was about 0.97 GB and flat, and the host still reported about 33 GB
available. That also means the 32 GiB from the first run shouldn't be read as the hardware spec for every run here.

The logs made things look worse than they were, too: 668 stale warnings, 334 distinct Operators, every line logged
twice. Those were the workers whose identities existed on the Gateway but had never made it to their processes.
Count log lines as failed workers and you've doubled your failure count.

## One connection, and a result we can recover

I replaced the Operator's multi-request HTTP enrollment with a bootstrap WebSocket exchange. The Operator sends its
request, gets back the request ID, immediately sends its proof of possession, and waits on that same connection for
the owner's decision and the credential bundle. Sending the proof before approval takes a round trip off the
approved path. WebSockets start life as an HTTP upgrade, sure, but the Operator no longer uses the REST enrollment,
startup reauthentication, CA-fetch, or renewal calls from earlier.

For automatic deployment approval, bootstrap uses `wss://` with an owner-pinned CA. After enrollment, the worker
connects outbound over the mTLS pub/sub transport for commands, heartbeats and certificate renewal. The Operator
still listens on nothing. The Gateway's owner-facing HTTP and CLI interfaces stay as they were; this is about the
Operator's side of the boundary.

A WebSocket alone doesn't fix delivery. If the socket drops after issuance commits, the same running client
reconnects with its existing in-memory token, keys and CSRs. The Gateway resumes the request and hands over the same
identity without another approval, so retryable issuance or downstream session failures can recover without minting
a second identity. CREATE_SESSION is still its own governed envelope. I didn't fold it into issuance to make this
work.

Identity now lives in the Operator process's memory. Enrollment writes no certificate, private key, requester token,
or signer-trust files to the runtime directory. An explicitly supplied CA file is read-only. Local audit and
execution state still persist. A network drop preserves the live process's identity; stopping the process or
rebooting its host loses it, and that means fresh enrollment and owner approval. So Gateway restart and Operator
restart now mean very different things.

Certificate renewal goes over mTLS pub/sub and updates the in-memory certificate that later connections use. L2
policy and signer keys arrive in the enrollment bundle and refresh at renewal. That isn't instant revocation
propagation: a running worker keeps that signer trust until renewal or re-enrollment.

Tracing the approved state turned up correctness bugs, not just transport cost. Automatic approval had trusted the
first request that announced a given launch ID, and that ID was observable. Now it checks the request's CSR
fingerprints against the fingerprints the actual worker recorded before connecting. A launch ID correlates events.
It doesn't prove ownership.

Issuance now rechecks that the approving owner is still active. Redeeming a completed request preserves existing
sessions instead of overwriting them into an active state. Lifecycle transitions have one validated writer, and any
write leaving the issuing state has to name the current issuance-lease holder, so a late operation from a reclaimed
lease can't complete or roll back its successor's work. Rollback also gets a usable context after an issuance
deadline expires.

All of that has failure-injection and boundary tests: dropped delivery after commit, reconnect with the same
identity, session preservation, expired leases, inactive owners, renewal followed by reconnect. The final full
`make ci` pass was reported on October 11. That's correctness evidence alongside the live run, not a stand-in for
it.

## Liveness without rewriting identity

Heartbeat writes and stale-state transitions were still on my list at this point. Heartbeats now record last-seen
telemetry in the observed storage tier, separate from bound identity, and reads overlay it. A worker's effective
stale status is derived from its last sign of life and its declared heartbeat interval. Reading it doesn't write an
active-to-stale transition or move the bound root.

The Gateway no longer runs a stale timer for every Operator. It hands out a derived `stale_at` deadline, and the
console uses heartbeat events plus that deadline to update its view. Identity lookup and authentication stay on
their indexed paths, away from liveness derivation. Same lesson the profiles kept teaching: an identity read should
not reconcile the fleet.

## Every bundle delivered, and still not a ready fleet

The next 2,000 attempt failed after 82.9 seconds. This time all 2,000 bundles got delivered, with none of the old
completion EOFs or response-write errors. Instead, deployment rejected a worker whose command subscription was
announced as stale. No soak, no fan-out, and teardown stopped all 2,000.

Request-to-bundle timings showed the tail:

| Measure | Request created → credential bundle delivered |
| --- | --- |
| Minimum | 15.8 s |
| p50 | 37.5 s |
| p90 | 51.2 s |
| p99 | 58.8 s |
| Maximum | 60.6 s |

Five requests went over 60 seconds. Those durations start at request creation, not at issuance, and that
distinction mattered, because my first explanation was wrong. I assumed the never-heartbeated worker's stale clock
also started at request creation. It doesn't. `claimed_at` is stamped at issuance. The interval that matters runs
from issuance through bundle delivery, first connection, and command subscription.

The evidence said that path was getting overloaded by issuing the whole fleet at once. The last bundle arrived
roughly 39 seconds after the last issuance, before the worker had done the rest of its connection work. I didn't
isolate every contributor to the tail, and request-to-bundle percentiles alone don't prove which worker crossed its
issuance-based deadline. What they did show is that delivering every identity wasn't enough, and the failed
subscription is where readiness actually broke.

## Approve at the rate workers become ready

Deployment still stages and validates the whole cohort. But approval now advances through a sliding window of at
most 256 approved workers waiting for command readiness. A worker gives its slot back only when the Gateway
announces its command subscription. The window refills once at least half of it is free, or when whatever's left in
the queue fits.

No pacing sleeps, no approval-status polling loop. The readiness events we already had drive progress. Refilling in
decent-sized batches keeps the number of governed decisions somewhere around 16 for 2,000 workers instead of one per
worker; the exact count depends on how readiness trickles in. Every batch keeps fingerprint binding and
transactional validation.

So, about that rule I wrote down after the gates. Cutting per-enrollment work got rid of scans, redundant writes,
and misconfigured database connections. It didn't make every downstream stage infinitely concurrent. What separates
this window from the gates I tore out is what it holds a slot for: until the worker can actually receive a command.
Issuance can't sprint arbitrarily far ahead of connection establishment. A semaphore around the database call
releases far too early to enforce that.

## The 2,000-Operator run that passed

It ran from 20:44:55 to 20:52:30 PDT on October 10: 2,000 native workers on one local host against one Gateway, one
deployment invocation, a staging parallelism limit of 2,048, and the new 256-worker approval window. About 7 minutes
36 seconds end to end.

| Phase | Measured result |
| --- | --- |
| Gateway startup | 1.10 s |
| Deployment/enrollment through command readiness | 54.82 s; 2,000 workers |
| Idle soak | About 5 minutes; every recorded sample had 2,000 active, 0 inactive, 0 stale |
| Fan-out | 2,001/2,001 succeeded; 0 failures |
| Post-fan-out settle observation | About 42 s; every recorded sample still had 2,000 active and 0 stale |
| Gateway process restart | 1.15 s |
| Fleet recovery after restart | 32.82 s; 0 sessions rotated |
| Fan-out after recovery | 2,001/2,001 succeeded; 0 failures |
| Harness teardown | 5.57 s; all 2,000 workers stopped |

The soak and settle report has 32 observations, all healthy. The largest recorded heartbeat age was 31.98 seconds
against the default 30-second interval. They're samples, so they don't prove there was never a blip between them.

The fleet test dispatches a small host-name command (`uname -n` on this Linux run) through the governed CLI path.
Both rounds use concurrency 2,001 and include the embedded Operator:

| Round | Wall | p50 | p95 | p99 | Max | Failed |
| --- | --- | --- | --- | --- | --- | --- |
| After soak | 3.890 s | 1.552 s | 2.934 s | 3.043 s | 3.881 s | 0 |
| After Gateway recovery | 2.318 s | 0.870 s | 1.562 s | 1.576 s | 2.316 s | 0 |

The second round was faster, but those are two different points in one lifecycle, not repeated steady-state
trials, and I haven't worked out why. Same caution for the 54.82 seconds: that's deployment wall time including
staging and readiness, so don't hold it up against the in-process, completion-only ~110/s from earlier.

Recovery stayed at about one heartbeat interval at both sizes: 32.0 seconds for 1,000, 32.82 for 2,000. The Gateway
process itself restarts in just over a second. The longer number is waiting for every worker to reconnect, heartbeat,
and re-establish its command subscription. No sessions rotated, and the post-recovery dispatch is there to check that
those workers can do more than show up in the registry.

## What 2,000 processes cost

The resource CSVs hold 45 samples at roughly ten-second intervals. I converted the Linux memory counters to binary
units. Sampled peaks can miss shorter spikes, and the final sample is after the Gateway restart but before teardown.

| Resource | Successful N=2,000 run |
| --- | --- |
| Gateway RSS, highest sample | 1.65 GiB |
| Gateway RSS, final sample after restart | 1.12 GiB |
| Gateway file descriptors, highest sample | 6,054 |
| Gateway threads, highest sample | 25 |
| Fleet PSS, final and highest sample | 23.89 GiB total; about 12.23 MiB per Operator |
| Fleet threads, final and highest sample | 24,831 |
| Gateway database, final sample | 277.36 MiB |
| Gateway WAL, highest sample | 2.39 GiB |
| Gateway WAL, final sample after restart | 4.83 MiB |

PSS splits shared pages across processes, which makes it more honest here than adding up 2,000 RSS figures and
counting the shared binary 2,000 times. It still describes this run, not a fixed per-Operator memory budget.

The Gateway footprint wasn't flat across this short run. RSS climbed during the early soak, sat around 1.47 GiB late
in it, and hit its sampled peak before restart. Fleet PSS grew over the lifecycle too. That doesn't establish a
leak, but it also doesn't let me borrow the first attempt's five-hour stability claim for this build. It needs a
longer run to find the plateau.

Disk needs its own budget. By the early soak the WAL had hit 2.39 GiB, and it stayed at that sampled size until the
restart, way bigger than the main database. WAL file size on its own doesn't tell me how much work was still
uncheckpointed. It does tell me how much space the test ate. Once a run has filled the root volume on you, keeping
evidence and provisioning scratch space stops being housekeeping and becomes part of running a valid experiment.

## What I'm carrying into the next test

The thing I'll keep is the difference between requested, approved, issued, delivered, subscribed, and heartbeating.
When the system is healthy those counts agree and nobody thinks about them. Under load they came apart, and every
gap pointed at a different owner: the writer, response delivery, the worker's connection path, the readiness
observer. A registry full of issued identities was never enough to call the fleet ready.

The fixtures only got useful once they reproduced the whole failing path. Completion-only tests missed
authentication. Enrollment-only success missed credential delivery. Delivery success missed command readiness. Each
live failure handed me the missing boundary for the next regression test.

Profiling took out the history-dependent work, and it also turned up a config bug worth roughly 8x. With a pooled
driver, checking one connection proves nothing about the others. With the race detector on, a throughput number
answers the wrong question. With duplicated logs, so does a line count.

And the lifecycle plumbing mattered as much as the hot path. Repeating a test took headless startup, safe
deployment replacement, event-driven readiness, scoped cleanup, and evidence that survives a failure. All of it
lives in the production deployment code, because that's the code the scale test runs.

## Where I stopped

Local qualification passed at 2,000: enrollment, five-minute idle soak, governed fan-out, Gateway restart recovery,
a second fan-out, teardown. The detailed 1,000-Operator numbers are from an earlier build, and the final 2,000
evidence is one full successful lifecycle. Neither tells you a maximum fleet size.

Next is more Operators on remote hosts, still behind one Gateway. Native SSH deployment, session-bound streaming and
mixed-target orchestration are implemented, but the WSL host and mixed-host hardware smokes haven't run yet. They'll
put real network latency and different host scheduling under a path this experiment only ever exercised locally.

There's more to measure on this box too: a longer soak on the final build, repeated cold 2,000-Operator runs, heavier
execution and inference workloads, proper tail-latency attribution, and governed bulk shutdown at fleet size. And
nothing here, all small commands under doctrine posture, says anything yet about consensus-quorum or human-approval
latency.

