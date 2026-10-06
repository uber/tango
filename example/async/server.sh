#!/usr/bin/env bash
# Starts or stops the example Tango server for the async RPC tests.
#   server.sh start [memory|disk]   disk keeps job status across restarts
#   server.sh stop                  sends SIGINT, so running jobs record FAILED
source "$(dirname "$0")/lib.sh"

CONFIG="$REPO/example/tango-config.yaml"
BACKUP="$WORK/tango-config.yaml.orig"
DISK_ROOT="$WORK/storage"

start() {
  if port_open; then echo "a server already listens on $ADDR" >&2; exit 1; fi
  if [ "${1:-memory}" = disk ]; then
    [ -f "$BACKUP" ] || cp "$CONFIG" "$BACKUP"
    DISK_ROOT="$DISK_ROOT" perl -pi -e 's|^  type: "memory"|  type: "disk"\n  disk:\n    root_path: "$ENV{DISK_ROOT}"|' "$CONFIG"
  fi
  (cd "$REPO" && nohup make run-server >"$WORK/server.log" 2>&1 &)
  local end=$((SECONDS + 600))
  until port_open; do
    if [ "$SECONDS" -ge "$end" ]; then echo "server did not start; see $WORK/server.log" >&2; exit 1; fi
    sleep 1
  done
  echo "server listens on $ADDR (storage: ${1:-memory}, log: $WORK/server.log)"
}

stop() {
  pkill -INT -f 'example_/example$' || true
  local end=$((SECONDS + 60))
  while port_open; do
    if [ "$SECONDS" -ge "$end" ]; then echo "server did not stop" >&2; exit 1; fi
    sleep 0.5
  done
  if [ -f "$BACKUP" ]; then mv "$BACKUP" "$CONFIG"; fi
  echo "server stopped"
}

case "${1:-}" in
  start) start "${2:-memory}" ;;
  stop) stop ;;
  *) echo "usage: $0 start [memory|disk] | stop" >&2; exit 2 ;;
esac
