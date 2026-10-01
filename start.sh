#!/bin/bash
set -euo pipefail
# Docker runs two independent processes. If either exits, restart the pair.
receipt-upload database init
receipt-upload serve &
web_pid=$!
receipt-upload worker &
worker_pid=$!
stop() {
  trap - TERM INT EXIT
  kill -TERM "$web_pid" "$worker_pid" 2>/dev/null || true
  wait "$web_pid" 2>/dev/null || true
  wait "$worker_pid" 2>/dev/null || true
}
trap stop TERM INT EXIT
set +e
wait -n "$web_pid" "$worker_pid"
status=$?
exit "$status"
