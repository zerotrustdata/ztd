#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Record one governed agent session as a video, unattended. It runs the real
# `ztd mcp agent run <client>` against a throwaway Gateway and the fictional
# Zero Trust Paper Company, inside VHS (https://github.com/charmbracelet/vhs),
# then shows the signed receipts, the audit chain check, and `ztd tui` on the
# same Gateway. Nothing is simulated: the video is the terminal.
#
#   make vhs CLIENT=goose PROMPT="I am Priya in finance. Which invoice is past due?"
#   make vhs CLIENT=goose THINK=1 PROMPT="..."   # adds the smallest thinking model scene
#   bash scripts/demo-record.sh goose "I am Priya in finance. Which invoice is past due?"
#
# A fixed persona scenario from demos/vhs-scenarios.json (client, prompt, THINK,
# the API calls the model is told about, and the title card come from the file):
#   make vhs SCENARIO=priya-finance
#   make vhs-scenarios                 # every scenario, one take each (scripts/demo-scenarios.sh)
#
# Needs on PATH: vhs, ttyd, ffmpeg, a Chromium/Chrome (ROD_BROWSER_BIN to
# choose one), python3, an ssh-agent holding a key (same as the smoke), and the
# agent binary. goose also needs Ollama already serving the model (free, CPU).
# The defaults are the smallest models that work, on purpose: qwen2.5:0.5b is the
# smallest that really emits tool calls through goose. THINK=1 adds a scene
# with qwen3:0.6b, the smallest thinking model: it thinks every time but rarely
# tool-calls through goose, so it runs through the Gateway's governed inference
# (embedded operator) and its signed thinking is shown. Models often answer
# badly; that is part of the demo, since the governance is the point.
# DEMO_MODEL and DEMO_THINK_MODEL override.
# DEMO_TRIBUNAL=<model> binds the Gateway Tribunal panel to that model under
# the consensus posture (experimental: tiny voters often deny the call).
# Claude, Codex, Gemini and Devin need their own paid or signed-in accounts.
# Their non-interactive flags below are unverified; only goose was exercised.
#
# This script does not download models, and it does not call the hidden
# `ztd operator model` commands removed in v1.8.0. goose talks to Ollama
# directly. The default agent tag qwen2.5:0.5b is not in
# eval/model-pull-catalog.json (the catalog tag is qwen2.5:0.5b-instruct), so
# `ztd eval models pull` cannot stage it; it has to already be resident.
# qwen3:0.6b is a catalog tag. When the Inference Operator does not already
# serve it, stage it with `ztd eval models pull qwen3:0.6b`, which dispatches
# the typed OLLAMA_MODEL_PULL command. THINK=1 and DEMO_TRIBUNAL pass that
# same Ollama to the Gateway with --inference-ollama-endpoint. `ztd eval models
# freeze` inventories the provider through the typed inventory command.
#
# DEMO_AGENT_TIMEOUT (seconds, default 180) is how long the tape waits for the
# agent to print its exit code; vhs discards the whole take when it expires, so
# raise it on a CPU-only host (a small model there can need 240s or more).
# DEMO_ATTEMPTS (default 10) is how many takes are tried for a signed tool call.
#
# Output (default build/demos/<time>-<client>/): demo.mp4, demo.txt (text
# frames), prompt.txt, tape, vhs.log (the winning attempt's log; every attempt
# keeps vhs-attempt-<n>.log), receipts.json (only the receipts the winning
# attempt added; receipts-all.json has every attempt's), and recording.json,
# which binds the video's sha256 to the audit chain head it was recorded against.
#
# Bash 3.2 compatible (macOS /bin/bash).

set -euo pipefail
shopt -u patsub_replacement 2>/dev/null || true # bash 5.2: keep "&" literal in the tape fill

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PAPER="$REPO_ROOT/demos/zero-trust-paper-company/paper.py"

SCEN_FILE="$REPO_ROOT/demos/vhs-scenarios.json"
SCENARIO="${DEMO_SCENARIO:-}"

CLIENT="${1:-}"
PROMPT="${2:-}"
THINK="${DEMO_THINK:-}"
TITLE=""
MODEL="${DEMO_MODEL:-qwen2.5:0.5b}"
THINK_MODEL="${DEMO_THINK_MODEL:-qwen3:0.6b}"
TIMEOUT="${DEMO_AGENT_TIMEOUT:-180}"

die() { echo "ERROR: $*" >&2; exit 1; }
log() { echo "==> $*"; }

# Scenario fields come from the checked-in file; an explicit client, prompt or
# THINK still wins.
scn() {
    python3 - "$SCEN_FILE" "$SCENARIO" "$1" "$PAPER" <<'PY'
import json, sys
path, sid, field, paper = sys.argv[1:5]
for s in json.load(open(path))["scenarios"]:
    if s["id"] == sid:
        break
else:
    sys.exit("unknown scenario: " + sid + " (see demos/vhs-scenarios.json)")
if field == "hint":
    print(" or ".join(json.dumps([paper, "call", c["method"], c["path"], "--role", c["role"]], separators=(",", ":")) for c in s["calls"]))
elif field == "think":
    print(1 if s["think"] else 0)
else:
    print(s[field])
PY
}
HINT_CALLS=""
if [ -n "$SCENARIO" ]; then
    [ -n "$CLIENT" ] || CLIENT="$(scn client)" || exit 1
    [ -n "$PROMPT" ] || PROMPT="$(scn prompt)" || exit 1
    [ -n "$THINK" ] || THINK="$(scn think)" || exit 1
    TITLE="$(scn title)" || exit 1
    HINT_CALLS="$(scn hint)" || exit 1
fi
THINK="${THINK:-0}"
if [ -z "$CLIENT" ] || [ -z "$PROMPT" ]; then
    die "usage: demo-record.sh <client> \"<prompt>\"  or  DEMO_SCENARIO=<id> demo-record.sh  (clients: claude codex devin gemini goose)"
fi
[ -n "$TITLE" ] || TITLE="Zero Trust Paper Company: a governed $CLIENT session"
TITLE="$(printf '%s' "$TITLE" | tr '"' "'")"
for tool in vhs ttyd ffmpeg python3 ssh-add; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required on PATH (VHS needs ttyd and ffmpeg; see demos/README.md)"
done
ssh-add -l >/dev/null 2>&1 || die "no ssh-agent key: start one (eval \"\$(ssh-agent -s)\"; ssh-add) as the smoke does"
command -v "$CLIENT" >/dev/null 2>&1 || die "$CLIENT is not on PATH"

# goose and the governed inference scenes share one Ollama. The script never
# pulls; a missing tag has to be resident already (see the header).
OLLAMA_HOST="${OLLAMA_HOST:-http://127.0.0.1:11434}"
export OLLAMA_HOST
if [ "$CLIENT" = goose ] || [ "$THINK" = 1 ] || [ -n "${DEMO_TRIBUNAL:-}" ]; then
    command -v curl >/dev/null 2>&1 || die "curl is required to check that Ollama is serving"
    curl -fsS -o /dev/null "$OLLAMA_HOST/api/tags" \
        || die "Ollama is not serving at $OLLAMA_HOST. This script does not download models. goose needs $MODEL already resident (not a catalog tag). A missing THINK model is staged with: ztd eval models pull $THINK_MODEL"
fi

# What the viewer sees typed: the real launch command, per client.
# claude/gemini/devin/codex flags are unverified guesses. Only goose was run.
case "$CLIENT" in
    goose) agent_args="--provider ollama --model $MODEL --max-turns 4 -t" ;;
    claude|gemini|devin) agent_args="-p" ;;
    codex) agent_args="exec" ;;
    *) die "unsupported client: $CLIENT" ;;
esac

[ -x "$REPO_ROOT/ztd" ] || make -C "$REPO_ROOT" build-go
OUT="${DEMO_OUT:-$REPO_ROOT/build/demos}/${DEMO_NAME:-$(date +%Y%m%d-%H%M%S)-${SCENARIO:-$CLIENT}}"
ROOT="${DEMO_ROOT:-$OUT/root}"
mkdir -p "$OUT" "$ROOT/home" "$ROOT/bin"
OUT="$(cd "$OUT" && pwd)"
ROOT="$(cd "$ROOT" && pwd)"

paper_pid=""
cleanup() {
    status=$?
    [ -z "$paper_pid" ] || kill "$paper_pid" 2>/dev/null || true
    (cd "$ROOT" && "$REPO_ROOT/ztd" gw stop >/dev/null 2>&1) || true
    exit "$status"
}
trap cleanup EXIT

# The fictional company: reset to the opening dataset, then serve it.
if [ -f "$REPO_ROOT/demos/zero-trust-paper-company/.demo-state/company.sqlite3" ]; then
    python3 "$PAPER" reset --confirm fictional-data >/dev/null
else
    python3 "$PAPER" init >/dev/null
fi
python3 "$PAPER" serve >"$OUT/paper.log" 2>&1 &
paper_pid=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    python3 "$PAPER" call GET /health >/dev/null 2>&1 && break
    sleep 1
done
python3 "$PAPER" call GET /health >/dev/null 2>&1 || die "paper company did not start (see $OUT/paper.log)"

# Throwaway Gateway root, started off camera. --accept-unrecoverable skips the
# recovery-key ceremony, which needs a terminal the recorder does not have.
export HOME="$ROOT/home"
ln -sf "$REPO_ROOT/ztd" "$ROOT/bin/ztd"
export PATH="$ROOT/bin:$PATH"
umask 077
cp "$REPO_ROOT/.env.example" "$ROOT/.env"
chmod 600 "$ROOT/.env"
log "starting the demo Gateway in $ROOT"
gw_args=(--accept-unrecoverable)
if [ "$THINK" = 1 ] || [ -n "${DEMO_TRIBUNAL:-}" ]; then
    gw_args+=(--capabilities "data,inference,observer" --inference-ollama-endpoint "$OLLAMA_HOST")
fi
if [ -n "${DEMO_TRIBUNAL:-}" ]; then
    gw_args+=(--posture consensus)
fi
(cd "$ROOT" && ztd gw start "${gw_args[@]}" >"$OUT/gw-start.txt" 2>&1) || die "gateway start failed (see $OUT/gw-start.txt)"
[ -z "${DEMO_TRIBUNAL:-}" ] || (cd "$ROOT" && ztd eval models freeze && ztd consensus enroll tribunal --model "$DEMO_TRIBUNAL") >"$OUT/tribunal.txt" 2>&1 || die "tribunal bind failed (see $OUT/tribunal.txt)"

# The prompt is a file so the video shows it whole and no shell quoting is
# involved. A small local model also needs to be told how the company's tools
# are invoked; Claude, Codex and Gemini do not (DEMO_HINT=0).
{
    printf '%s\n' "$PROMPT"
    if [ "${DEMO_HINT:-$([ "$CLIENT" = goose ] && echo 1 || echo 0)}" = 1 ]; then
        # The three default calls, or the calls the selected scenario lists.
        [ -n "$HINT_CALLS" ] || HINT_CALLS="[\"$PAPER\",\"call\",\"GET\",\"/accounting/past-due\",\"--role\",\"finance\"] or [\"$PAPER\",\"call\",\"GET\",\"/inventory/stock\",\"--role\",\"ops\"] or [\"$PAPER\",\"call\",\"GET\",\"/documents/search?q=Northstar\",\"--role\",\"procurement\"]"
        printf '\n%s\n' "You work for the fictional Zero Trust Paper Company. Query its API with run_shell_command (command python3). Valid args: $HINT_CALLS. Use the one that answers the question."
    fi
} >"$ROOT/prompt.txt"

cmd="ztd mcp agent run $CLIENT -- $agent_args \"\$(cat prompt.txt)\"; echo \"agent exit code: \$?\""
tape="$(cat "$REPO_ROOT/demos/tapes/agent-run.tape")"
tape="${tape//\{\{OUT\}\}/$OUT}"
tape="${tape//\{\{CLIENT\}\}/$CLIENT}"
tape="${tape//\{\{TITLE\}\}/$TITLE}"
tape="${tape//\{\{TIMEOUT\}\}/$TIMEOUT}"
think_scene=""
if [ "$THINK" = 1 ]; then
    think_scene="$(cat "$REPO_ROOT/demos/tapes/think-scene.tape")"
    think_scene="${think_scene//\{\{MODEL\}\}/$THINK_MODEL}"
fi
tape="${tape//\{\{THINK_SCENE\}\}/$think_scene}"
tape="${tape//\{\{CMD\}\}/$cmd}"
printf '%s\n' "$tape" >"$OUT/demo.tape"

# A tiny local model sometimes returns nothing or a malformed call, so retry
# until this attempt added a signed tool-call receipt, completed or refused.
# Whether the answer is any good is not checked. All attempts share one root, so
# the receipt ids present before an attempt tell its receipts from earlier ones.
receipts_json() {
    (cd "$ROOT" && ztd audit event-receipts --app "spiffe://ztd.local/app/$CLIENT" --json) 2>/dev/null || true
}
# Prints MCP_CALL transaction ids that are not in the newline-separated list $1.
new_calls() {
    receipts_json | BEFORE="$1" python3 -c '
import json, os, sys
try:
    data = json.load(sys.stdin)
except ValueError:
    sys.exit(0)
before = set(os.environ["BEFORE"].split())
for r in data.get("event_receipts") or []:
    if r.get("action_type") == "MCP_CALL" and r.get("transaction_id") not in before:
        print(r["transaction_id"])
'
}
attempt=1
while :; do
    before_ids="$(new_calls "")"
    log "recording attempt $attempt/${DEMO_ATTEMPTS:-10} (agent timeout ${TIMEOUT}s) -> $OUT/demo.mp4"
    (cd "$ROOT" && vhs "$OUT/demo.tape") >"$OUT/vhs-attempt-$attempt.log" 2>&1 || true
    [ -f "$OUT/demo.mp4" ] && [ -n "$(new_calls "$before_ids")" ] && break
    [ -f "$OUT/demo.mp4" ] && mv "$OUT/demo.mp4" "$OUT/failed-attempt-$attempt.mp4"
    [ "$attempt" -lt "${DEMO_ATTEMPTS:-10}" ] || die "no tool-call receipt after $attempt attempts (see $OUT/vhs-attempt-*.log)"
    attempt=$((attempt + 1))
done
cp "$OUT/vhs-attempt-$attempt.log" "$OUT/vhs.log"

# Evidence: bind the video to the audit chain it was recorded against.
cp "$ROOT/prompt.txt" "$OUT/prompt.txt"
# receipts-all.json is every attempt's receipts; receipts.json keeps only the
# ones the winning attempt added, so it matches the video's attempt.
receipts_json >"$OUT/receipts-all.json"
BEFORE="$before_ids" OUT="$OUT" python3 - <<'PY'
import json, os
out = os.environ["OUT"]
before = set(os.environ["BEFORE"].split())
data = json.load(open(os.path.join(out, "receipts-all.json")))
data["event_receipts"] = [r for r in data.get("event_receipts") or [] if r.get("transaction_id") not in before]
json.dump(data, open(os.path.join(out, "receipts.json"), "w"), indent=2)
PY
(cd "$ROOT" && ztd audit verify) >"$OUT/audit-verify.txt"
grep -q 'Audit chain OK' "$OUT/audit-verify.txt" || die "audit verify did not report Audit chain OK"
if command -v sha256sum >/dev/null 2>&1; then sum="$(sha256sum "$OUT/demo.mp4")"; else sum="$(shasum -a 256 "$OUT/demo.mp4")"; fi
OUT="$OUT" SCENARIO="$SCENARIO" TITLE="$TITLE" ATTEMPTS="$attempt" CLIENT="$CLIENT" MODEL="$MODEL" THINK="$THINK" THINK_MODEL="$THINK_MODEL" SUM="${sum%% *}" ztd_version="$(ztd --version 2>/dev/null | head -n 1)" python3 - <<'PY'
import json, os, re
out = os.environ["OUT"]
verify = open(os.path.join(out, "audit-verify.txt")).read()
head = re.search(r"Head: seq=(\d+) hash=([0-9a-f]+)", verify)
receipts = json.load(open(os.path.join(out, "receipts.json")))
txs = sorted({m for m in re.findall(r'"transaction_id":\s*"([0-9a-f]{64})"', json.dumps(receipts))})
json.dump({
    "scenario": os.environ["SCENARIO"] or None,
    "title": os.environ["TITLE"],
    "attempts": int(os.environ["ATTEMPTS"]),
    "client": os.environ["CLIENT"],
    "model": os.environ["MODEL"] if os.environ["CLIENT"] == "goose" else None,
    "think": os.environ["THINK"] == "1",
    "think_model": os.environ["THINK_MODEL"] if os.environ["THINK"] == "1" else None,
    "ztd": os.environ["ztd_version"],
    "video": "demo.mp4",
    "video_sha256": os.environ["SUM"],
    "audit_head_seq": int(head.group(1)) if head else None,
    "audit_head_hash": head.group(2) if head else None,
    "receipt_transaction_ids": txs,
}, open(os.path.join(out, "recording.json"), "w"), indent=2)
PY
log "done: $OUT/demo.mp4 (sha256 ${sum%% *})"
