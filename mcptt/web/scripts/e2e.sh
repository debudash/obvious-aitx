#!/usr/bin/env bash
# Console e2e against a real server: build + run the Go server on a throwaway
# database, seed the demo roster, run Playwright, then tear everything down.
# Run from mcptt/web (or anywhere; paths are resolved from this script).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"   # mcptt/
WEB="$ROOT/web"
SRV="$ROOT/server"

# Go may live outside PATH (e.g. /usr/local/go in dev sandboxes).
if ! command -v go >/dev/null 2>&1 && [[ -x /usr/local/go/bin/go ]]; then
  export PATH="/usr/local/go/bin:$PATH"
fi

TMPDIR_E2E="$(mktemp -d)"
SRV_PID=""

cleanup() {
  if [[ -n "$SRV_PID" ]]; then
    kill "$SRV_PID" 2>/dev/null || true
  fi
  rm -rf "$TMPDIR_E2E"
}
trap cleanup EXIT

echo "== building server =="
(cd "$SRV" && go build -o "$TMPDIR_E2E/mcptt-server" ./cmd/server)

echo "== starting server on :8080 (db: $TMPDIR_E2E/mcptt-e2e.db) =="
MCPTT_ADDR=":8080" \
MCPTT_DB_PATH="$TMPDIR_E2E/mcptt-e2e.db" \
MCPTT_JWT_SECRET="${MCPTT_JWT_SECRET:-e2e-secret-0123456789abcdef0123456789abcdef}" \
  "$TMPDIR_E2E/mcptt-server" &
SRV_PID=$!

for i in $(seq 1 50); do
  curl -fsS http://localhost:8080/healthz >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS http://localhost:8080/healthz >/dev/null || { echo "server did not become healthy"; exit 1; }

echo "== seeding demo roster =="
(
  cd "$SRV"
  MCPTT_DB_PATH="$TMPDIR_E2E/mcptt-e2e.db" \
  MCPTT_JWT_SECRET="${MCPTT_JWT_SECRET:-e2e-secret-0123456789abcdef0123456789abcdef}" \
    go run ./cmd/seed
)

cd "$WEB"
npx playwright test "$@"
