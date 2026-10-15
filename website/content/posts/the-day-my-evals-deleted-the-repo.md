# The day my evals deleted the repo

*Field notes, October 14, 2026.*

Woke up to my PC having rebooted itself overnight. Annoying, but fine. Then I looked at the project directory.

The root was gone.

An agent evaluation assignment bug had wiped the root of the repo. Not a file, not a folder. The root.

`git restore`.

Everything came back. Thank you, git. Thirty seconds of pure fear, then mostly embarrassment.

## The seatbelt was bolted to the floor

Once my heart rate came down, I noticed the part that actually stung: **my evals weren't running through L1–L5.**

I'm building a zero-trust governance pipeline whose whole job is "the model can ask, but it cannot directly act."
My own evaluation harness had been quietly skipping it. L1 Doctrine is the layer that screens for destructive
intent, and it would have stopped this exact thing. I had a seatbelt bolted to the floor next to the seat.

That changed how I think about evals. They can't just check whether an agent plays its role convincingly. They
have to run under the same governance as everything else, or they aren't measuring anything real.

## Perfect timing

So naturally I picked a Wednesday to do a massive refactor of the eval framework, with potential employers looking
at a public mirror that kept going up and down. Peak strategy.

About that mirror: I put it up so people could watch me build in public. Honestly, I think it mostly showed
instability. Every restart meant someone could land on a 502. I'm looking at a better setup for
[OpenDevOps.ai](https://opendevops.ai) so you can always browse what ran and when, without catching me mid-restart.

## Demo videos as QA

The rest of the day went to demo videos. I got OBS Studio set up and used the recordings as a manual end-to-end
test of v1.1.0. Recording a demo turns out to be a great QA tool, because nothing finds rough edges like knowing
other people will watch.

It turned up scratch pads and temp files scattered all over the filesystem, so I'm fixing those now. Everything
is moving under the runtime directory where it belongs.

## Score for the day

- Lost the repo root, then got it back ✅
- Found a real gap in my own governance coverage ✅
- Refactored the eval framework ✅
- Shipped a mirror that looked like it was on fire ✅
- Made demo videos and found bugs ✅

Pretty good for a day that started with `rm`-shaped trauma.
