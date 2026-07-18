#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
if [ ! -f routeragent.pid ]; then
    echo "routeragent.pid not found, skip stop."
    exit 0
fi
pid="$(cat routeragent.pid)"
if [ -z "$pid" ]; then
    echo "routeragent.pid is empty, skip stop."
    exit 0
fi
kill -TERM "$pid"
