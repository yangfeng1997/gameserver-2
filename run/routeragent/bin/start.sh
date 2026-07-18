#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
LOG_DIR="../log"
STDOUT_LOG="$LOG_DIR/routeragent.stdout.log"
STDERR_LOG="$LOG_DIR/routeragent.stderr.log"
mkdir -p "$LOG_DIR"
: > "$STDOUT_LOG"
: > "$STDERR_LOG"
exec ./routeragent --nodeid 1.6.0 --daemon 1>>"$STDOUT_LOG" 2>>"$STDERR_LOG"
