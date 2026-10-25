#!/usr/bin/env bash
# ===========================================================================
#
# sro-gm.sh - create a GM test character on a local OpenSRO world
#
# Copies sro-gm/main.go into the OpenSRO checkout's git-ignored
# apps/server/.state/sro-gm/ and runs it from apps/server (Go lets only code
# inside the OpenSRO server module import its internal packages).
#
#   ./sro-gm.sh --opensro ~/opensro --restart -account gm -name "[GM]Test"
#
# --restart stops the fleet first and redeploys afterwards, so the GM grant
# is live when it finishes. Without it, stop the GameWorld yourself.
# Every other argument goes to the tool; run with -help to list them.
#
# ===========================================================================
set -euo pipefail

opensro="."
restart=0
args=()
while [ $# -gt 0 ]; do
	case "$1" in
		--opensro) opensro="$2"; shift 2 ;;
		--restart) restart=1; shift ;;
		*) args+=("$1"); shift ;;
	esac
done

here="$(cd "$(dirname "$0")" && pwd)"
server="$(cd "$opensro" && pwd)/apps/server"
if [ ! -f "$server/go.mod" ]; then
	echo "$opensro is not an OpenSRO checkout (no apps/server/go.mod). Pass --opensro <path>." >&2
	exit 1
fi
mkdir -p "$server/.state/sro-gm"
cp "$here/sro-gm/main.go" "$server/.state/sro-gm/main.go"

cd "$server"
if [ "$restart" = 1 ]; then
	echo "Stopping the fleet..."
	go run ./cmd/operations/sro-nomad stop
fi
status=0
go run ./.state/sro-gm "${args[@]}" || status=$?
if [ "$restart" = 1 ]; then
	# Redeploy even after a refusal, so the world never stays down.
	echo "Redeploying..."
	go run ./cmd/operations/sro-nomad deploy -build
fi
exit "$status"
