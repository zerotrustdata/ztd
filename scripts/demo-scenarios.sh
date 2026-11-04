#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Record the fixed persona scenarios in demos/vhs-scenarios.json, one take each,
# with scripts/demo-record.sh, for a human to review and keep the good ones.
# Local only: no CI or release workflow runs this, and it needs a local Ollama.
#
#   make vhs-scenarios                           # every scenario, in file order
#   make vhs-scenarios ONLY="priya-finance morgan-compliance"
#
# Output: build/demos/scenarios-<time>/INDEX.md plus one <n>-<scenario id>/
# directory per take (demo.mp4, recording.json, receipts.json, ...). A scenario
# that never gets a signed tool-call receipt is listed as FAILED and the rest
# still run. Exits non-zero if any failed.
#
# Bash 3.2 compatible (macOS /bin/bash).

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SCEN_FILE="$REPO_ROOT/demos/vhs-scenarios.json"

ids="$*"
[ -n "$ids" ] || ids="$(python3 -c 'import json,sys; print(" ".join(s["id"] for s in json.load(open(sys.argv[1]))["scenarios"]))' "$SCEN_FILE")" || exit 1

OUT="${DEMO_OUT:-$REPO_ROOT/build/demos}/scenarios-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT"
export DEMO_OUT="$OUT"
failed=0
n=0
for id in $ids; do
    n=$((n + 1))
    echo "==> scenario $n: $id"
    start=$(date +%s)
    if DEMO_SCENARIO="$id" DEMO_NAME="$n-$id" bash "$SCRIPT_DIR/demo-record.sh" "" ""; then
        echo "ok" >"$OUT/$n-$id.status"
    else
        echo "FAILED" >"$OUT/$n-$id.status"
        failed=$((failed + 1))
    fi
    echo $(($(date +%s) - start)) >"$OUT/$n-$id.seconds"
done

OUT="$OUT" IDS="$ids" SCEN_FILE="$SCEN_FILE" python3 - <<'PY'
import json, os, subprocess
out = os.environ["OUT"]
byid = {s["id"]: s for s in json.load(open(os.environ["SCEN_FILE"]))["scenarios"]}
rows = []
for n, sid in enumerate(os.environ["IDS"].split(), 1):
    s = byid.get(sid, {"title": sid, "domain": "", "prompt": "", "shows": ""})
    d = os.path.join(out, "%d-%s" % (n, sid))
    status = open(d + ".status").read().strip() if os.path.exists(d + ".status") else "FAILED"
    secs = open(d + ".seconds").read().strip() if os.path.exists(d + ".seconds") else "?"
    rec, calls, video = {}, "-", "-"
    if os.path.exists(d + "/recording.json"):
        rec = json.load(open(d + "/recording.json"))
        try:
            receipts = json.load(open(d + "/receipts.json"))["event_receipts"]
            calls = ", ".join("%s" % r.get("target_resource", "?") for r in receipts)
        except (OSError, ValueError, KeyError):
            pass
        video = "[demo.mp4](%d-%s/demo.mp4)" % (n, sid)
    rows.append((n, s, status, secs, rec, calls, video))
with open(os.path.join(out, "INDEX.md"), "w") as f:
    f.write("# Demo scenarios\n\nOne take per persona. Watch each, keep the good ones, delete the rest.\n\n")
    f.write("| # | Persona and task | Domain | Video | THINK | Attempts | Signed tool calls | Audit | Wall time |\n|---|---|---|---|---|---|---|---|---|\n")
    for n, s, status, secs, rec, calls, video in rows:
        f.write("| %d | %s | %s | %s | %s | %s | %s | %s | %ss |\n" % (
            n, s["title"], s["domain"], video, "yes" if rec.get("think") else "no", rec.get("attempts", "-"),
            calls, "chain OK, head seq %s" % rec["audit_head_seq"] if rec else status, secs))
    f.write("\n## Prompts and what a good take shows\n\n")
    for n, s, status, secs, rec, calls, video in rows:
        f.write("%d. **%s** (`%s`)\n   - Prompt: %s\n   - Shows: %s\n" % (n, s["title"], s.get("id", ""), s["prompt"], s["shows"]))
print("index: " + os.path.join(out, "INDEX.md"))
PY
rm -f "$OUT"/*.status "$OUT"/*.seconds
[ "$failed" -eq 0 ] || { echo "ERROR: $failed scenario(s) failed (see $OUT/INDEX.md)" >&2; exit 1; }
