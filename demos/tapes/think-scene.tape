# Optional scene (THINK=1): the same model through the Gateway's governed
# inference, where its thinking is captured, signed, and shown separately.
Hide
Type "clear"
Enter
Type "ztd eval models freeze > /dev/null 2>&1"
Enter
Show
Type "# Governed inference: {{MODEL}} thinks, the Gateway receipts it"
Enter
Type 'ztd eval quickstart --model {{MODEL}} 2>&1 | grep -v INFO > quick.txt; grep -E "^(Platform|Model|Run) " quick.txt; echo "quickstart exit code: $?"'
Enter
Wait+Screen@150s /quickstart exit code: [0-9]/
Sleep 2s
Type 'ztd eval runs trace $(grep -o "runs trace [^ ]*" quick.txt | awk "{print \$3}") --scenario route-primary-ownership 2>&1 | grep -v INFO | cut -c1-150 | head -24'
Enter
Wait+Screen@60s /Text:/
Sleep 6s
