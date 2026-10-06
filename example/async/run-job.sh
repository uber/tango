#!/usr/bin/env bash
# Submits one async job and polls it until it is COMPLETED or FAILED.
#   run-job.sh changed [first-ref] [second-ref] [--bypass] [--timeout N]
#   run-job.sh graph [ref] [--bypass] [--timeout N]
# Refs are git refs of this repo. --timeout sets the submit deadline, which is the job timeout.
source "$(dirname "$0")/lib.sh"

kind="${1:?usage: $0 changed|graph [refs...] [--bypass] [--timeout N]}"; shift
bypass=false timeout=1800 refs=()
while [ $# -gt 0 ]; do
  case "$1" in
    --bypass) bypass=true ;;
    --timeout) timeout="$2"; shift ;;
    *) refs+=("$1") ;;
  esac
  shift
done

case "$kind" in
  changed) req=$(changed_req "${refs[0]:-main~1}" "${refs[1]:-main}" "$bypass"); method=ChangedTargetsJob ;;
  graph) req=$(graph_req "${refs[0]:-main}" "$bypass"); method=TargetGraphJob ;;
  *) echo "unknown kind: $kind" >&2; exit 2 ;;
esac

echo "request: $req"
start=$SECONDS
submitted=$(call "Submit$method" "$timeout" "$req")
echo "submit: $(state "$submitted") token=$(token "$submitted")"
out=$(poll_until_terminal "Poll$method" "$(poll_req "$(token "$submitted")")")
echo "poll:   $(state "$out") after $((SECONDS - start))s"
echo "items:  $(count_items "$out")"
grep -A3 '"error"' <<<"$out" || true
