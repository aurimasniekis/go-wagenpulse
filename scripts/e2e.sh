#!/usr/bin/env bash
# Runs the Playwright suite (Chromium + axe, light and dark) against a fresh, throwaway WagenPulse instance.
#
# The instance runs in development mode on a free localhost port with a temporary data directory (the development
# KEK is generated inside it). Nothing touches .wagenpulse/ or any real data; everything is deleted afterwards.
# All data the tests create is synthetic (see web/e2e/fixtures.ts).
#
#   scripts/e2e.sh                 build the UI and the binary, then run every spec
#   SKIP_BUILD=1 scripts/e2e.sh    reuse bin/wagenpulse (it must already embed the current UI)
#   scripts/e2e.sh pages.spec.ts   extra arguments go to `playwright test`
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [[ -z "${SKIP_BUILD:-}" ]]; then
  make build >/dev/null
fi
bin="$root/bin/wagenpulse"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/wagenpulse-e2e.XXXXXX")
pid=""
cleanup() {
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
port=$(free_port)
mport=$(free_port)

export WAGENPULSE_MODE=development
export WAGENPULSE_DATA_DIR="$tmp/data"
export WAGENPULSE_PUBLIC_URL="http://localhost:$port"
export WAGENPULSE_HTTP_LISTEN="127.0.0.1:$port"
export WAGENPULSE_MANAGEMENT_LISTEN="127.0.0.1:$mport"
export WAGENPULSE_LOG_LEVEL=warn

"$bin" serve >"$tmp/server.log" 2>&1 &
pid=$!

for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$mport/readyz" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "wagenpulse exited during startup:" >&2
    cat "$tmp/server.log" >&2
    exit 1
  fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$mport/readyz" >/dev/null

export WGP_E2E_URL="http://localhost:$port"
export WGP_E2E_BIN="$bin"
export WGP_E2E_AUTH_DIR="$tmp/auth"

status=0
(cd web && pnpm exec playwright test "$@") || status=$?
if [[ $status -ne 0 ]]; then
  echo "--- server log (last 80 lines) ---" >&2
  tail -n 80 "$tmp/server.log" >&2
fi
exit $status
