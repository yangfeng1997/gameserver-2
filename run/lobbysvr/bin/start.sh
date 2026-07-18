#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
LOG_DIR="../log"
STDOUT_LOG="$LOG_DIR/lobbysvr.stdout.log"
STDERR_LOG="$LOG_DIR/lobbysvr.stderr.log"
mkdir -p "$LOG_DIR"
: > "$STDOUT_LOG"
: > "$STDERR_LOG"
exec ./lobbysvr --nodeid 1.2.0 --daemon 1>>"$STDOUT_LOG" 2>>"$STDERR_LOG"
