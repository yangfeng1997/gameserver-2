#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1
echo "=== clean log routeragent ==="
rm -rf ./routeragent/log
mkdir -p ./routeragent/log
echo "=== clean log gatesvr ==="
rm -rf ./gatesvr/log
mkdir -p ./gatesvr/log
echo "=== clean log lobbysvr ==="
rm -rf ./lobbysvr/log
mkdir -p ./lobbysvr/log
ret=0
echo "=== start routeragent ==="
./routeragent/bin/start.sh
code=$?
if [ "$code" -ne 0 ]; then
    ret=$code
fi
echo "=== start gatesvr ==="
./gatesvr/bin/start.sh
code=$?
if [ "$code" -ne 0 ]; then
    ret=$code
fi
echo "=== start lobbysvr ==="
./lobbysvr/bin/start.sh
code=$?
if [ "$code" -ne 0 ]; then
    ret=$code
fi
exit "$ret"
