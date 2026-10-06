#!/usr/bin/env bash
# Restarts the example server with fresh in-memory storage and checks the async RPC behavior.
# Prints one PASS or FAIL line for each check and exits non-zero if any check fails.
source "$(dirname "$0")/lib.sh"

failures=0
check() { # check <name> <expected-regex> <actual>
  if [[ "$3" =~ ^($2)$ ]]; then echo "PASS  $1: $3"; else echo "FAIL  $1: got $3, want $2"; failures=$((failures + 1)); fi
}

"$(dirname "$0")/server.sh" stop >/dev/null
"$(dirname "$0")/server.sh" start memory

req=$(changed_req main~3 main~2)
out=$(call SubmitChangedTargetsJob 1800 "$req")
tok=$(token "$out")
poll=$(poll_req "$tok")
check "submit returns RUNNING" JOB_STATE_RUNNING "$(state "$out")"
check "submit returns a token" "[0-9a-f]{32}" "$tok"
check "poll while the job runs" JOB_STATE_RUNNING "$(state "$(call PollChangedTargetsJob 60 "$poll")")"
check "second submit returns the same token" "$tok" "$(token "$(call SubmitChangedTargetsJob 1800 "$req")")"

out=$(poll_until_terminal PollChangedTargetsJob "$poll")
check "poll after the job ends" JOB_STATE_COMPLETED "$(state "$out")"
check "the result comes with the status" "yes" "$(grep -q '"changedTargets":' <<<"$out" && grep -q '"metadata":' <<<"$out" && echo yes || echo no)"

check "submit after COMPLETED returns COMPLETED" JOB_STATE_COMPLETED "$(state "$(call SubmitChangedTargetsJob 1800 "$req")")"

bypass=$(changed_req main~3 main~2 true)
out=$(call SubmitChangedTargetsJob 1800 "$bypass")
check "bypass_cache is a separate job" "yes" "$([ "$(token "$out")" != "$tok" ] && echo yes || echo no)"
check "bypass_cache job completes" JOB_STATE_COMPLETED "$(state "$(poll_until_terminal PollChangedTargetsJob "$(poll_req "$(token "$out")")")")"

check "poll of an unknown token is not found" ERROR:ERROR_USER "$(state "$(call PollChangedTargetsJob 60 "$(poll_req 00000000000000000000000000000000)")")"

graph=$(graph_req main~1)
out=$(call SubmitTargetGraphJob 1800 "$graph")
check "target graph submit" JOB_STATE_RUNNING "$(state "$out")"
out=$(poll_until_terminal PollTargetGraphJob "$(poll_req "$(token "$out")")")
check "target graph poll" JOB_STATE_COMPLETED "$(state "$out")"
check "target graph result comes with the status" "yes" "$(grep -q '"targets":' <<<"$out" && echo yes || echo no)"

"$(dirname "$0")/server.sh" stop >/dev/null
echo "$failures check(s) failed"
exit "$failures"
