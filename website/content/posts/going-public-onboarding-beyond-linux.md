# Going public, and finding out onboarding only worked on Linux

*Field notes, October 12–13, 2026.*

I finally squashed the history and made the ztd repo public. All the private commits went into one, `ztd v1.0.0`,
and [github.com/zerotrustdata/ztd](https://github.com/zerotrustdata/ztd) is open.

Then I tried to ship it. That took four more tags than it should have.

## Three tags that never shipped

Releases 1.0.1 through 1.0.3 were skipped because the CD pipeline was broken, each time in a new and exciting place:

| Tag | What happened |
| --- | --- |
| v1.0.1 | Tagged on a commit whose `VERSION` still said v1.0.0. Preflight rejected it before anything built. That one's on me. |
| v1.0.2 | Binaries built and signed. Then the workflow failed re-verifying the artifacts it had just downloaded. Nothing published. |
| v1.0.3 | Built, signed, verified. Then the upload to R2 failed. Nothing published. |

v1.0.4 was the first one to make it all the way to the [Releases page](/releases/). The three dead tags still have
release notes, each one saying plainly that it has no release. A hole in the version history with no explanation
is worse than an embarrassing explanation.

The pipeline got three changes out of it. Releases get rehearsed before anything is tagged. The R2 upload writes the
release manifest last, so a half-uploaded set never shows up as a version. And every run, rehearsals included, now
writes, reads back and deletes a probe object in the bucket before it builds anything. A release pipeline you've
never run end to end is a hypothesis.

## It worked on my machine

v1.0.5 was supposed to be the onboarding release. A one-command install. Release pages with checksums and Sigstore
verification steps. A `.env` for secrets and endpoints. `make up-public`, so Operators on other hosts can enroll.
Every command the CLI tells you to run printed alone on its own line, so you can actually copy it.

I mostly work on Linux, and all of that was tested on Linux. Thankfully, I dug out an old MacBook and a Windows PC
and tried it there too.

Onboarding on both was shit.

## Agents as first-time users

I didn't want to fix only the things I tripped over, because I know this system too well to trip over the right
things. So I had agents run through the guides as a user, on Windows and on Mac: follow the docs as written, and
report every place they got stuck. Then I fixed what they found. Some of it:

- **Windows:** stopping a local Operator left its child processes running. Operators now run inside a Job Object
  that kills everything in it when it closes.
- **Windows:** drive-letter paths (`D:/x/y`) got mangled in evaluation scenario workspaces.
- **Windows:** line endings. The repo now pins LF everywhere except PowerShell scripts, which get CRLF.
- **macOS:** the first time the Gateway reads its master key, Keychain asks for approval. A background `gw start`
  treated that wait as a failure. Now it tells you it's waiting and gives you five minutes to click Allow.
- **macOS:** the port conflict check only looked at one address family, and SSH ProxyCommand processes died
  mid-handshake.

And my favorite: fixing the port check for macOS broke Linux. The new probe disabled `SO_REUSEADDR` on every Unix
host, so on Linux the TIME_WAIT sockets left by a Gateway that had just stopped made its own ports look busy.
Restart the Gateway and it would wander off to 8087/8450, while every CLI command kept dialing 8080/8443 and got
`connection refused`. Lovely. The probe now keeps `SO_REUSEADDR` on Linux, and the CLI reads the bound ports from
the running Gateway instead of assuming the defaults.

All of this shipped in v1.0.6.

## One command, still clunky

I also built a quick launch for [zerotrustdata.io](https://zerotrustdata.io):

```sh
curl -fsSL https://zerotrustdata.io/start.sh | sh
```

It installs the checksum-verified binary into `~/ztd`, starts a Gateway (or reuses the one that's running), signs
the CLI in, and runs a first governed action through the Gateway and an Operator. Rerunning it is safe. It never
asks for sudo. It covers Linux and macOS; Windows doesn't get a one-liner yet.

It works. It's still too clunky. I don't know yet what the right version looks like, so that one's TBD.

## Closing the loop

The [last field note](/build-in-public/enrolling-two-thousand-operators/) started with a question from a senior
infrastructure engineer about what one Gateway does when a thousand Operators enroll against it. What I didn't say
is that the question came during my round 3 interview, at a company I'm really excited about. I didn't have an answer in
the room.

I sent the results from the scale test to the recruiter to pass along to them. Hoping to hear back soon.
