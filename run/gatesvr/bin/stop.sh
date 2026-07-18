#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
if [ ! -f gatesvr.pid ]; then
    echo "gatesvr.pid not found, skip stop."
    exit 0
fi
pid="$(cat gatesvr.pid)"
if [ -z "$pid" ]; then
    echo "gatesvr.pid is empty, skip stop."
    exit 0
fi
kill -TERM "$pid"
