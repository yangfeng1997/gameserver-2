#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
LOG_DIR="../log"
STDOUT_LOG="$LOG_DIR/gatesvr.stdout.log"
STDERR_LOG="$LOG_DIR/gatesvr.stderr.log"
mkdir -p "$LOG_DIR"
: > "$STDOUT_LOG"
: > "$STDERR_LOG"
exec ./gatesvr --nodeid 1.1.0 --daemon 1>>"$STDOUT_LOG" 2>>"$STDERR_LOG"
