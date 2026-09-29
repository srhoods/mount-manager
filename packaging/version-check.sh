#!/bin/bash
# Refuse to package changed sources under an unchanged version.
#   version-check.sh check   -> exit 1 (with reason) if the sources changed but VERSION did not
#   version-check.sh record  -> remember the built VERSION + source hash
set -euo pipefail
cd "$(dirname "$0")/.."
V=$(tr -d ' \n' < VERSION)
[[ $V =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "VERSION must be MAJOR.MINOR.PATCH (got '$V')" >&2; exit 1; }
STATE=packaging/.release-state
# Everything that ends up in an RPM (UI build output and this state file excluded).
HASH=$( { find agent/mmd.c agent/Makefile agent/tests server/go.mod server/go.sum server/cmd server/internal web/src web/index.html web/package.json web/package-lock.json \
            packaging/agent packaging/server packaging/cli -type f ! -path 'server/internal/ui/dist/*' ! -name test_mount ! -name '*.o' 2>/dev/null | LC_ALL=C sort | xargs sha256sum; } | sha256sum | cut -d' ' -f1)
case "${1:-check}" in
record) echo "$V $HASH" > "$STATE"; echo "recorded $V ($HASH)"; exit 0;;
check)
  [ -f "$STATE" ] || { echo "version-check: no baseline yet; $V will become the baseline"; exit 0; }
  read -r OV OH < "$STATE"
  if [ "$HASH" = "$OH" ]; then echo "version-check: sources unchanged since $OV"; exit 0; fi
  if [ "$V" = "$OV" ]; then
    echo "version-check: FAILED - sources changed since $OV was built, but VERSION is still $OV." >&2
    echo "  Bump VERSION (minor for features, patch for fixes) and add a '## <version>' entry to CHANGELOG.md." >&2
    exit 1
  fi
  [ "$(printf '%s\n%s\n' "$OV" "$V" | sort -V | tail -1)" = "$V" ] || { echo "version-check: FAILED - VERSION $V is lower than the last built $OV" >&2; exit 1; }
  grep -q "^## $V\$" CHANGELOG.md || { echo "version-check: FAILED - CHANGELOG.md has no '## $V' entry" >&2; exit 1; }
  echo "version-check: $OV -> $V";;
*) echo "usage: $0 check|record" >&2; exit 2;;
esac
