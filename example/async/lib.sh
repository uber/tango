# Shared helpers for the async RPC test scripts. Source this file.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ADDR="${TANGO_ADDR:-127.0.0.1:8081}"
REMOTE="${TANGO_REMOTE:-https://github.com/uber/tango.git}"
WORK="${TANGO_TEST_DIR:-/tmp/tango-async-test}"
GRPCURL="$WORK/bin/grpcurl"
mkdir -p "$WORK/bin"

ensure_grpcurl() {
  [ -x "$GRPCURL" ] || GOBIN="$WORK/bin" go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
}

sha() { git -C "$REPO" rev-parse "$1"; }

# changed_req <first-ref> <second-ref> [bypass]
changed_req() {
  jq -cn --arg r "$REMOTE" --arg a "$(sha "$1")" --arg b "$(sha "$2")" --argjson bypass "${3:-false}" \
    '{first_revision:  {strategy: "COMPUTATION_STRATEGY_UNSET", remote: $r, base_sha: $a},
      second_revision: {strategy: "COMPUTATION_STRATEGY_UNSET", remote: $r, base_sha: $b},
      bypass_cache: $bypass}'
}

# graph_req <ref> [bypass]
graph_req() {
  jq -cn --arg r "$REMOTE" --arg a "$(sha "$1")" --argjson bypass "${2:-false}" \
    '{build_description: {strategy: "COMPUTATION_STRATEGY_UNSET", remote: $r, base_sha: $a}, bypass_cache: $bypass}'
}

# poll_req <token>
poll_req() { jq -cn --arg t "$1" '{token: $t}'; }

# call <Method> <timeout-seconds> <request-json>; prints grpcurl output, never fails.
call() {
  ensure_grpcurl
  (cd "$REPO" && "$GRPCURL" -plaintext -import-path proto -proto tango.proto \
    -H 'rpc-caller: tango-async-test' -H 'rpc-service: tango' -H 'rpc-encoding: proto' \
    -max-msg-sz 1073741824 -max-time "$2" -d "$3" "$ADDR" "uber.tango.Tango/$1" 2>&1) || true
}

# state <grpcurl output>: prints the job state, or ERROR:<code> for an RPC error.
state() {
  local s
  s=$(grep -o 'JOB_STATE_[A-Z]*' <<<"$1" | head -1 || true)
  if [ -n "$s" ]; then echo "$s"; return; fi
  s=$(grep -o '"code": "[A-Z_]*"' <<<"$1" | head -1 | cut -d'"' -f4 || true)
  [ -n "$s" ] || s=$(grep -o 'Code: [A-Za-z]*' <<<"$1" | head -1 | cut -d' ' -f2 || true)
  echo "ERROR:${s:-$(head -1 <<<"$1" | cut -c1-120)}"
}

# token <grpcurl output>: prints the job token of a submit response.
token() { grep -o '"token": "[0-9a-f]*"' <<<"$1" | head -1 | cut -d'"' -f4 || true; }

# count_items <grpcurl output>: prints 1 for each field that the poll response has, and 0 for each other field.
count_items() {
  printf 'status=%s targets=%s changed_targets=%s metadata=%s\n' \
    "$(grep -c '"status":' <<<"$1" || true)" "$(grep -c '"targets":' <<<"$1" || true)" \
    "$(grep -c '"changedTargets":' <<<"$1" || true)" "$(grep -c '"metadata":' <<<"$1" || true)"
}

# poll_until_terminal <PollMethod> <poll-request-json> [max-seconds]; prints the final output.
poll_until_terminal() {
  local out st end=$((SECONDS + ${3:-600}))
  while :; do
    out=$(call "$1" 60 "$2")
    st=$(state "$out")
    case "$st" in
      JOB_STATE_COMPLETED|JOB_STATE_FAILED|ERROR:*) echo "$out"; return ;;
    esac
    if [ "$SECONDS" -ge "$end" ]; then echo "$out"; return; fi
    sleep 2
  done
}

port_open() { lsof -nP -iTCP:"${ADDR##*:}" -sTCP:LISTEN >/dev/null 2>&1; }
