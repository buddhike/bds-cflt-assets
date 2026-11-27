#!/usr/bin/env bash
# Copies libs/design/graphite.css into every app that uses it, at
# <app>/frontend/src/graphite.css. An app's Docker build context is its own
# directory, so it cannot read this file from here: it keeps a copy.
#
#   libs/design/sync.sh           write the copies
#   libs/design/sync.sh --check   fail if a copy differs (what CI runs)
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
src="$root/libs/design/graphite.css"
apps=(demos/launchpad demos/pims demos/automated-api-diagnosis demos/lightning-inventory)

status=0
for app in "${apps[@]}"; do
  dest="$root/$app/frontend/src/graphite.css"
  if [ "${1:-}" = "--check" ]; then
    if ! cmp -s "$src" "$dest"; then
      echo "$app/frontend/src/graphite.css differs from libs/design/graphite.css: run libs/design/sync.sh" >&2
      status=1
    fi
  else
    cp "$src" "$dest"
    echo "wrote $app/frontend/src/graphite.css"
  fi
done
exit $status
