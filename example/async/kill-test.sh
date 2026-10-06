#!/usr/bin/env bash
# Checks that the status of a job that a killed server left reads as not found after it goes stale.
# Uses disk storage, so the status survives the restart. Restores the example config at the end.
source "$(dirname "$0")/lib.sh"
server="$(dirname "$0")/server.sh"
trap '"$server" stop >/dev/null' EXIT

"$server" stop >/dev/null
rm -rf "$WORK/storage"
"$server" start disk

req=$(changed_req main~7 main~6)
out=$(call SubmitChangedTargetsJob 1800 "$req")
poll=$(poll_req "$(token "$out")")
echo "submit: $(state "$out")"
before=$(state "$(call PollChangedTargetsJob 60 "$poll")")
echo "poll before the kill: $before"
if [ "$before" != JOB_STATE_RUNNING ]; then
  echo "INCONCLUSIVE: the job was not running at the kill"; exit 1
fi

pkill -KILL -f 'example_/example$'
until ! port_open; do sleep 0.5; done
"$server" start disk
echo "poll right after restart: $(state "$(call PollChangedTargetsJob 60 "$poll")")"
sleep 20
after=$(state "$(call PollChangedTargetsJob 60 "$poll")")
echo "poll after the status goes stale: $after"
if [ "$after" = ERROR:ERROR_USER ]; then
  echo "PASS"
else
  echo "FAIL: want ERROR:ERROR_USER (not found)"; exit 1
fi
