#!/bin/bash
# Parse agent tool calls and results from sandbox pod logs.
# Usage: ./hack/parse-agent-steps.sh <pod-name> [namespace] [max-output-lines]
set -euo pipefail

POD="${1:?Usage: $0 <pod-name> [namespace] [max-output-lines]}"
NS="${2:-openshift-lightspeed}"
MAX_OUTPUT="${3:-10}"

oc logs "$POD" -n "$NS" | awk -v max_output="$MAX_OUTPUT" '
/INFO lightspeed_agentic: \[agent\] Starting query/ {
    sub(/.*\[agent\] /, "")
    printf "--- %s ---\n\n", $0
    turn = 0
    in_result = 0
    next
}
/INFO lightspeed_agentic: \[provider:run\] tool_use:/ {
    if (in_result) { printf "\n"; in_result = 0 }
    turn++
    sub(/.*\[provider:run\] tool_use: /, "")
    printf "Step %d: %s\n", turn, $0
    next
}
/INFO lightspeed_agentic: \[provider:run\] tool_result:/ {
    sub(/.*\[provider:run\] tool_result: /, "")
    printf "     → %s\n", $0
    in_result = 1
    result_lines = 0
    next
}
/INFO lightspeed_agentic: \[provider:run\] output:/ {
    if (in_result) { printf "\n"; in_result = 0 }
    sub(/.*\[provider:run\] output: /, "")
    printf "\nOutput: %s\n", substr($0, 1, 500)
    next
}
/INFO lightspeed_agentic: \[provider:run\] result:/ {
    if (in_result) { printf "\n"; in_result = 0 }
    sub(/.*\[provider:run\] result: /, "")
    printf "Result: %s\n", $0
    next
}
in_result {
    if (/^DEBUG |^INFO |^Tracing is disabled|^Processing output|^Setting current|^No conversation|^Calling LLM|^Starting turn|^Turn .* complete/) next
    if (/^Wall time:/ || /^Process exited with/ || /^Output:$/) next
    result_lines++
    if (result_lines <= max_output) printf "       %s\n", $0
    if (result_lines == max_output + 1) printf "       ... (truncated, use 3rd arg to show more)\n"
    next
}
'
