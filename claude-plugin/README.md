# ztd plugin for Claude Code

Puts a Claude Code session on the ztd governed path. The ztd Gateway MCP server becomes the only way for Claude to
read, change, or run anything. Every other tool is denied.

It works in every Claude Code surface that loads plugins, including the terminal CLI and the VS Code and JetBrains
extensions.

## What it does

| Component | Behavior |
| --- | --- |
| MCP server `ztd` ([.mcp.json](.mcp.json)) | Runs [scripts/mcp-stdio.sh](scripts/mcp-stdio.sh), which changes to your ztd root and runs `ztd mcp stdio --app <app_name>`. Every tool call is proxied over mTLS to the Gateway and becomes a `GovernanceEnvelope` that passes L1–L5. Tools appear as `mcp__plugin_ztd_ztd__<tool>`. |
| PreToolUse hook ([hooks/pretool.sh](hooks/pretool.sh)) | Denies every tool except the ztd tools, `ToolSearch`/`WaitForMcpServers` (needed to load MCP tools), and conversation-only tools (`Skill`, `TodoWrite`, `AskUserQuestion`, `EnterPlanMode`, `ExitPlanMode`). Unknown and future tools are denied. Fires in subagents too. |
| SessionStart hook ([hooks/session-start.sh](hooks/session-start.sh)) | Tells Claude it is governed, which tools to use, and the project path to pass to them. Runs `ztd mcp stdio --app <app_name> --check` locally and prints the app identity and next setup action. It exits successfully even when ztd is not installed yet. |
| `/ztd:setup` skill | Uses the SessionStart check and app identity to show only the remaining setup steps for the user to run in their own terminal. |

The hook does not grant anything. ztd tool calls still go through Claude Code's normal permission prompts. To skip
those prompts and rely on the Gateway's posture instead, add `"mcp__plugin_ztd_ztd"` to `permissions.allow` in your
settings.

## Requirements

- A ztd Gateway and an enrolled application identity (default name `claude`) in the configured ztd root. Under
  `doctrine` and `consensus`, `ztd auth enroll app <name>` approves itself when the owner is signed in. Under `ratify`
  and `notary`, or when no owner CLI identity is signed in, complete the sign-in and approval steps reported by the
  local `--check`. `/ztd:setup` guides the user through only the remaining steps.
- `/bin/sh` (Linux or macOS). See [Limitations](#limitations).

## Install

From GitHub:

```text
/plugin marketplace add zerotrustdata/ztd
/plugin install ztd@ztd
```

From a local checkout:

```text
/plugin marketplace add /path/to/ztd
/plugin install ztd@ztd
```

For development, load it for one session only:

```bash
claude --plugin-dir /path/to/ztd/claude-plugin
```

When the plugin is enabled, Claude Code asks for its options. `/config` changes them later:

| Option | Default | Meaning |
| --- | --- | --- |
| `ztd_root` | (required) | Directory that contains `.ztd/`, where you ran `ztd gw start` and enrolled. ztd resolves credentials from its working directory, so the bridge runs from here. |
| `ztd_binary` | `ztd` | Path to the `ztd` binary, or a name on `PATH`. |
| `app_name` | `claude` | Enrolled application identity. Receipts record it as `spiffe://ztd.local/app/<app_name>`. |

Install the plugin at project scope (`claude plugin install ztd@ztd --scope project`) when only some repositories should
be governed. Installed at user scope, it governs every session.

## Plugin or launcher

`ztd mcp agent run claude` is the other way to govern Claude Code. It starts the Gateway if needed, enrolls, and
launches Claude with `--strict-mcp-config` and `--disallowed-tools`.

| | Plugin | `ztd mcp agent run claude` |
| --- | --- | --- |
| Start | Any Claude Code surface, including IDE extensions | The `ztd` command, terminal only |
| Native tools | Denied by a hook. Still listed to the model, which is told not to use them. | Partly removed from the model's tool list. The disabled set is `nativeToolsToDisable` in [internal/cli/agent/config.go](../internal/cli/agent/config.go). |
| Tools not in that list | Denied (allowlist) | Not disabled |
| Other MCP servers | Their tools are denied by the hook | Not loaded (`--strict-mcp-config`) |
| Setup | You run `/ztd:setup` steps once | Automatic |

Use one or the other in a session. The plugin allows only its own server's tools, so with the plugin enabled the
launcher's `mcp__ztd__*` tools are denied. A server named `ztd` in a project's `.mcp.json` could run anything, so the
plugin does not trust that name.

## Limitations

- The governance boundary is the ztd tools. The hook stops Claude Code's own tools. It does not stop other processes on
  the machine, and it does not stop you: Claude Code's `!` shell commands and the IDE are outside it.
- Claude Code lets a tool call through if the hook cannot run: `sh` missing, a crash, or a timeout. The hook uses only
  `sh` builtins after reading its input. It does not run on Windows without a POSIX `sh`.
- Native tools remain visible to the model. Claude may try one; the hook denies it and Claude is told not to retry.
  Claude Code does not let a plugin remove built-in tools from the model's list. For that, launch with the launcher, or
  add the tools to `permissions.deny` in your own settings.
- `Skill` is allowed, so Claude can load skills from other plugins. A skill cannot act except through tools, and every
  tool call it makes passes through this hook.
- `Agent` is denied. A remote subagent would run where this plugin may not be installed.

## Develop

```bash
make claude-plugin-test   # hook and bridge-wrapper tests, shellcheck, claude plugin validate
```
