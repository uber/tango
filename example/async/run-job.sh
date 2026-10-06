#!/usr/bin/env bash
# Submits one async job and polls it until it is COMPLETED or FAILED.
#   run-job.sh changed [first-ref] [second-ref] [--bypass]
#   run-job.sh graph [ref] [--bypass]
# Refs are git refs of this repo.
source "$(dirname "$0")/lib.sh"

kind="${1:?usage: $0 changed|graph [refs...] [--bypass]}"; shift
bypass=false refs=()
while [ $# -gt 0 ]; do
  case "$1" in
    --bypass) bypass=true ;;
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
submitted=$(call "Submit$method" 60 "$req")
echo "submit: $(state "$submitted") token=$(token "$submitted")"
out=$(poll_until_terminal "Poll$method" "$(poll_req "$(token "$submitted")")")
echo "poll:   $(state "$out") after $((SECONDS - start))s"
echo "items:  $(count_items "$out")"
grep -A3 '"error"' <<<"$out" || true
