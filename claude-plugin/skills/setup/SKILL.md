---
name: setup
description: Walk the user through connecting the ztd plugin to their ztd Gateway. Use when the ztd MCP tools (mcp__plugin_ztd_ztd__*) are missing or failing to connect, or when the user asks how to set up ztd.
---

The user runs every step below in their own terminal, not through a tool. Native shell tools are denied in this session, and enrollment must not run through the governed path it sets up. Show the steps; do not try to run them.

Configured ztd root: `${user_config.ztd_root}`
Configured application identity: `${user_config.app_name}`

Give the user these steps, using the configured values above:

1. **Pick the ztd root.** ztd keeps its runtime state (credentials, trust bundle, audit data) in a `.ztd/` directory under the directory it runs from. Every command below runs from that same directory, and the plugin's `ztd_root` option must point at it.

2. **Start the Gateway** (skip if `ztd gw status` already reports it healthy):

   ```bash
   cd <ztd_root>
   ztd gw start --posture doctrine
   ```

   Posture is fixed for a running Gateway. `doctrine` enforces L1/L4/L5; `consensus`, `ratify`, and `notary` add L2 quorum and/or L3 human approval. `ztd gw start --help` lists the current choices.

3. **Enroll the human CLI identity.** The bridge uses it to receive L3 approval events.

   ```bash
   ztd auth enroll user
   ```

4. **Enroll the application identity Claude acts as**, then approve it as the owner in the ztd Console or with the command the enrollment prints:

   ```bash
   ztd auth enroll app <app_name>
   ztd auth enroll approve <request-id> --yes
   ```

5. **Set the plugin options** if they are wrong: `/config` in Claude Code, plugin `ztd`, set `ztd_root` and, if `ztd` is not on `PATH`, `ztd_binary`.

6. **Reconnect.** Run `/mcp` in Claude Code and reconnect the `plugin:ztd:ztd` server, or restart Claude Code. The server's log in `/mcp` shows the bridge's error if it still cannot connect.

If the user already launches Claude with `ztd mcp agent run claude`, that launcher performs steps 2 to 4 itself. Tell them to use the launcher or the plugin, not both in the same session: the plugin denies the launcher's `mcp__ztd__*` tools.
