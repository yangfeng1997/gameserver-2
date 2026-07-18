#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
if [ ! -f lobbysvr.pid ]; then
    echo "lobbysvr.pid not found, skip stop."
    exit 0
fi
pid="$(cat lobbysvr.pid)"
if [ -z "$pid" ]; then
    echo "lobbysvr.pid is empty, skip stop."
    exit 0
fi
kill -TERM "$pid"
