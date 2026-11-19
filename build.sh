#!/bin/sh
# Builds Falcon from source on macOS or Linux. The UI is prebuilt in web/dist: only Go and Git are needed.
#   ./build.sh          builds ./falcon
#   ./build.sh --web    rebuilds the UI first (needs Node.js)
set -eu
cd "$(dirname "$0")"
command -v go >/dev/null 2>&1 || { echo "Go is not installed: https://go.dev/dl/" >&2; exit 1; }
command -v git >/dev/null 2>&1 || echo "Falcon needs Git at run time." >&2
if [ "${1:-}" = "--web" ]; then (cd web && npm ci --no-audit --no-fund && npm run build); fi
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o falcon ./cmd/falcon
echo "Built ./falcon. Run:  ./falcon -open ~/code/project"
