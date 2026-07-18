#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
ret=0
echo "=== stop lobbysvr ==="
./lobbysvr/bin/stop.sh
code=$?
if [ "$code" -ne 0 ]; then
    ret=$code
fi
echo "=== stop gatesvr ==="
./gatesvr/bin/stop.sh
code=$?
if [ "$code" -ne 0 ]; then
    ret=$code
fi
echo "=== stop routeragent ==="
./routeragent/bin/stop.sh
code=$?
if [ "$code" -ne 0 ]; then
    ret=$code
fi
exit "$ret"
