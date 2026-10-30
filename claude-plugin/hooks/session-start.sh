#!/bin/sh
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# ztd SessionStart context. Plain stdout from a SessionStart hook is added to
# Claude's context. Enforcement does not depend on this script; pretool.sh
# denies non-gateway tools whether or not Claude has read this.

root=${CLAUDE_PLUGIN_OPTION_ZTD_ROOT:-}
app=${CLAUDE_PLUGIN_OPTION_APP_NAME:-claude}
bin=${CLAUDE_PLUGIN_OPTION_ZTD_BINARY:-ztd}
project=${CLAUDE_PROJECT_DIR:-$(pwd)}

case $app in
  '' | '${'* ) app=claude ;;
esac

cat <<EOF
This Claude Code session is governed by ztd (plugin "ztd").

- The only tools that can act are the ztd Gateway MCP tools, named mcp__plugin_ztd_ztd__<tool>. Load them with ToolSearch (query "ztd") before first use.
- Bash, Read, Edit, Write, Glob, Grep, NotebookEdit, WebFetch, WebSearch, Agent, and every other non-ztd tool are denied by a PreToolUse hook. A denial is final: do not retry the same tool, and do not look for another native tool that does the same thing.
- Every ztd tool call becomes a GovernanceEnvelope that passes L1-L5 at the Gateway and the executing Operator, and is recorded as application identity spiffe://ztd.local/app/${app}.
- ztd tools run on the Operator host, not inside this conversation. The project directory is ${project}: give read_file absolute paths, and pass working_dir "${project}" to run_shell_command.
- Read files with read_file. Change files with run_shell_command; there is no separate edit tool.
- Depending on the Gateway posture, a call may wait for a human to approve it in the ztd Console (L3). If a call is rejected by doctrine or policy, report the reason to the user instead of working around it.
- If no mcp__plugin_ztd_ztd__ tools are available, say so and ask the user to run /ztd:setup. Do not fall back to native tools.
EOF

# Claude does not substitute plugin userConfig values into skill text. Include
# the effective app name and the bridge's local setup check in hook context.
printf '\nSetup needed — next step:\nApp identity: %s\n' "$app"

# shellcheck disable=SC2016 # '${' matches an unsubstituted ${user_config.*} placeholder.
case $root in
  '' | '${'*)
    printf 'Configure the plugin ztd_root option to the directory where you run ztd.\n'
    ;;
  *)
    printf 'Configured ztd root: %s\n' "$root"
    if [ ! -d "$root" ]; then
      printf 'Create or choose the configured ztd root directory: %s\n' "$root"
    elif ! command -v "$bin" >/dev/null 2>&1; then
      printf 'Install ztd or set the plugin ztd_binary option to its executable.\n'
    else
      check_output=$(cd "$root" && "$bin" mcp stdio --app "$app" --check 2>&1)
      check_status=$?
      if [ "$check_status" -eq 0 ]; then
        printf 'Ready. %s\n' "$check_output"
      else
        printf '%s\n' "$check_output"
      fi
    fi
    ;;
esac
exit 0
