---
name: setup
description: Walk the user through connecting the ztd plugin to their ztd Gateway. Use when the ztd MCP tools (mcp__plugin_ztd_ztd__*) are missing or failing to connect, or when the user asks how to set up ztd.
---

The user runs setup commands in their own terminal. Native shell tools are denied in this session, and enrollment must not run through the governed path it sets up. Do not run commands.

Read the `Setup needed — next step:` block from the SessionStart context. Use its `App identity:` value as the app name. If the block is unavailable, use `claude`. Show only actions that remain according to that block:

- If `ztd_root` is unset, ask the user to set the plugin's `ztd_root` option in `/config` to the directory they use for ztd.
- If the configured root directory does not exist, have the user create or choose a valid `ztd_root` in `/config`.
- Follow the next command printed by `ztd mcp stdio --app <app> --check` in order. If it asks to start the Gateway, run `ztd gw start` from the `Configured ztd root:` in the block. If it asks for sign-in, use `ztd login` (or `ztd login web` under ratify or notary), then `ztd auth enroll app <app>`. Enrollment approves itself when the owner is signed in under doctrine or consensus. Under ratify or notary, complete approval in the Console or as directed by the enrollment command. If enrollment is pending, use the printed `ztd auth enroll approve` or resume with `ztd auth enroll app <app>`. If expired, re-enroll with `ztd auth enroll app <app>`.
- If the block says `Ready`, skip enrollment and proceed to reconnecting the MCP server.
- If the hook says the ztd binary is missing, have the user install it or set `ztd_binary` in `/config` before continuing.

After the remaining setup steps, reconnect `plugin:ztd:ztd` from `/mcp` or restart Claude Code. If the user already launches Claude with `ztd mcp agent run claude`, have them use that launcher or the plugin, not both in one session: the plugin denies the launcher's `mcp__ztd__*` tools.
