#!/bin/sh
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# shellcheck disable=SC2016 # '${' matches an unsubstituted ${user_config.*} placeholder.
# Starts the ztd MCP stdio bridge for the plugin.
#
# ztd resolves its .ztd/ runtime (enrolled credentials, trust bundle, Gateway
# URL) from the working directory, and Claude Code starts plugin MCP servers in
# the project directory. This script moves to the configured ztd root first.
#
# Usage: mcp-stdio.sh <ztd_root> <ztd_binary> <app_name>

root=$1
bin=$2
app=$3

fail() {
  printf 'ztd plugin: %s Run /ztd:setup in Claude Code for the setup steps.\n' "$1" >&2
  exit 1
}

# An unset userConfig option may reach us as the literal placeholder.
case $root in '' | '${'*) fail "the ztd_root option is not set." ;; esac
case $bin in '' | '${'*) bin=ztd ;; esac
case $app in '' | '${'*) app=claude ;; esac

[ -d "$root/.ztd" ] || fail "$root has no .ztd directory. Start the Gateway from that directory first ('ztd gw start')."
cd "$root" || fail "cannot enter $root."
command -v "$bin" >/dev/null 2>&1 || fail "ztd binary '$bin' was not found. Set the ztd_binary option to its full path."

exec "$bin" mcp stdio --app "$app"
