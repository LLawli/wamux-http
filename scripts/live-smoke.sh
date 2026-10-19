#!/usr/bin/env bash
# Runs the edge against a REAL wamux daemon, with a throwaway store, and
# checks the path up to a QR code from WhatsApp's servers: no phone pairs, no
# account is touched. Usage: WAMUX_BIN=/path/to/wamux scripts/live-smoke.sh
# The work directory (sockets, SQLite files, logs) defaults to a fresh one
# under /mnt/dados/caches/wamux-http-live; it is removed on success.
set -euo pipefail
cd "$(dirname "$0")/.."

: "${WAMUX_BIN:?set WAMUX_BIN to the wamux daemon binary}"
base=${LIVE_DIR_BASE:-/mnt/dados/caches/wamux-http-live}
mkdir -p "$base"
work=$(mktemp -d "$base/run.XXXXXX")
key=live-smoke-key-$RANDOM$RANDOM
port=${LIVE_PORT:-38080}
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$work/spool"
WAMUX_SOCKET_PATH="$work/wamux.sock" \
WAMUX_DATABASE_URL="sqlite://$work/core.db" \
WAMUX_MEDIA_SPOOL_DIR="$work/spool" \
WAMUX_LOG_LEVEL=info \
  "$WAMUX_BIN" >"$work/core.log" 2>&1 &
pids+=($!)
for _ in $(seq 100); do [ -S "$work/wamux.sock" ] && break; sleep 0.1; done
[ -S "$work/wamux.sock" ] || { echo "core did not open its socket"; tail -20 "$work/core.log"; exit 1; }

go build -o "$work/wamux-http" ./cmd/wamux-http
WAHA_API_KEY=$key PORT=$port \
WAMUX_HTTP_CORE_SOCKET="$work/wamux.sock" \
WAMUX_HTTP_DATABASE_URL="sqlite://$work/edge.db" \
WHATSAPP_FILES_FOLDER="$work/files" \
  "$work/wamux-http" >"$work/edge.log" 2>&1 &
pids+=($!)

api() { curl -sS -H "X-Api-Key: $key" -H 'Content-Type: application/json' "$@"; }
url=http://127.0.0.1:$port
for _ in $(seq 100); do curl -sf "$url/ping" >/dev/null && break; sleep 0.1; done

echo "== health"; api "$url/health"; echo
echo "== create and start"; api -X POST "$url/api/sessions" -d '{"name":"smoke","start":true}'; echo
status=""
for _ in $(seq 150); do
  status=$(api "$url/api/sessions/smoke" | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')
  [ "$status" = SCAN_QR_CODE ] && break
  sleep 0.2
done
echo "status: $status"
[ "$status" = SCAN_QR_CODE ] || { echo "no QR"; tail -30 "$work/edge.log" "$work/core.log"; exit 1; }
qr=$(api "$url/api/smoke/auth/qr?format=raw" | python3 -c 'import json,sys;print(json.load(sys.stdin)["value"])')
echo "QR from WhatsApp: ${qr:0:24}... (${#qr} chars)"
[ "${#qr}" -gt 40 ] || { echo "QR too short"; exit 1; }
png=$(api -o "$work/qr.png" -w '%{http_code} %{content_type} %{size_download}' "$url/api/smoke/auth/qr")
echo "PNG: $png"
case "$png" in "200 image/png "*) ;; *) echo "bad PNG answer"; exit 1;; esac
echo "== delete"; code=$(api -o /dev/null -w '%{http_code}' -X DELETE "$url/api/sessions/smoke"); echo "$code"
[ "$code" = 200 ] || exit 1
echo "== sessions after delete"; api "$url/api/sessions?all=true"; echo
cleanup
rm -rf "$work"
echo "live smoke passed"
