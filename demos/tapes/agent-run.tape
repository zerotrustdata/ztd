# VHS tape for one governed agent session against the Zero Trust Paper Company.
# Not run by hand: scripts/demo-record.sh fills the {{PLACEHOLDERS}} and runs
# `vhs` from the demo root, where prompt.txt, the Gateway, and `ztd` already
# exist. Everything on screen is the real CLI talking to the real Gateway.

Output "{{OUT}}/demo.mp4"
Output "{{OUT}}/demo.txt"

Require ztd

Set Shell "bash"
Set FontSize 16
Set Width 1400
Set Height 860
Set Padding 20
Set TypingSpeed 25ms
Set Theme "Catppuccin Mocha"

Hide
Type "export PS1='$ '"
Enter
Type "clear"
Enter
Show

# Scene 1: who is asking, and what.
Type "# {{TITLE}}"
Enter
Type "cat prompt.txt"
Enter
Sleep 3s

# Scene 2: the real launcher. Gateway, enrollment, lockdown, and the agent.
Type '{{CMD}}'
Enter
Wait+Screen@{{TIMEOUT}}s /agent exit code: [0-9]+/
Sleep 2s

{{THINK_SCENE}}

# Scene 3: the evidence the platform wrote while the agent worked.
Hide
Type "clear"
Enter
Show
Type "ztd audit event-receipts --app spiffe://ztd.local/app/{{CLIENT}}"
Enter
Wait+Screen@30s /event receipts/
Sleep 3s
Type "ztd audit verify"
Enter
Wait+Screen@30s /Audit chain OK/
Sleep 3s

# Scene 4: the same ledger, live, in the existing SSE-fed console.
Type "ztd tui"
Enter
Sleep 4s
Type "5"
Sleep 4s
Type "v"
Sleep 3s
Type "q"
Sleep 2s
