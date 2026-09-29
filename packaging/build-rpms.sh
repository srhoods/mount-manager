#!/bin/bash
# Build mountmgr-agent, mountmgr-server and mountmgr-cli RPMs at the version in ./VERSION.
#   RPM_HOST=root@host packaging/build-rpms.sh   (rpmbuild runs there, over ssh with nogit/id_ed25519)
#   packaging/build-rpms.sh                       (rpmbuild runs locally; needs an el9 toolchain)
# Steps: version check -> Go tests -> UI build -> Go build -> rpmbuild (agent runs its mount-logic tests in %check).
# Results land in build/rpm/. Set SKIP_TESTS=1 only for emergencies.
set -euo pipefail
cd "$(dirname "$0")/.."
packaging/version-check.sh check
V=$(tr -d ' \n' < VERSION)
[ -n "${SKIP_TESTS:-}" ] || { echo "== go tests"; (cd server && go vet ./... && go test -count=1 ./...); }
echo "== web ui"; (cd web && npm ci --silent && npm run build --silent)
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
echo "== go build $V"
(cd server && for b in mmserver mmctl; do CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$V" -o "$T/$b" ./cmd/$b; done)
CL="* $(LC_ALL=C date '+%a %b %d %Y') Steven <steven@rhoods.com> - $V-1
- Release $V; see CHANGELOG.md"
mkdir -p "$T/top/SOURCES" "$T/top/SPECS"
stage() { # name tarball-dir files...
  local n=$1; shift; mkdir -p "$T/$n-$V"; cp "$@" "$T/$n-$V/"
  tar -C "$T" -czf "$T/top/SOURCES/$n-$V.tar.gz" "$n-$V"
}
stage mountmgr-agent agent/mmd.c agent/Makefile packaging/agent/mountmgr-agent.service packaging/agent/agent.conf packaging/agent/mountmgr.sudoers
mkdir -p "$T/mountmgr-agent-$V/tests" && cp -r agent/tests/test_mount.c agent/tests/fakebin "$T/mountmgr-agent-$V/tests/"
tar -C "$T" -czf "$T/top/SOURCES/mountmgr-agent-$V.tar.gz" "mountmgr-agent-$V"
stage mountmgr-server "$T/mmserver" packaging/server/mountmgr-server.service packaging/server/server.env packaging/server/ldap.json.example
stage mountmgr-cli "$T/mmctl"
for s in agent/mountmgr-agent server/mountmgr-server cli/mountmgr-cli; do
  sed -e "s/@VERSION@/$V/" packaging/$s.spec | python3 -c "import sys;print(sys.stdin.read().replace('@CHANGELOG@', sys.argv[1]),end='')" "$CL" > "$T/top/SPECS/$(basename $s).spec"
done
mkdir -p build/rpm
if [ -n "${RPM_HOST:-}" ]; then
  SSH="ssh -i nogit/id_ed25519 -o BatchMode=yes"
  tar -C "$T/top" -czf - . | $SSH "$RPM_HOST" 'rm -rf /root/mmtop; mkdir /root/mmtop; tar xzf - -C /root/mmtop; for s in /root/mmtop/SPECS/*.spec; do rpmbuild --define "_topdir /root/mmtop" -ba $s > /root/mmtop/$(basename $s).log 2>&1 || { tail -30 /root/mmtop/$(basename $s).log; exit 1; }; done'
  scp -q -i nogit/id_ed25519 "$RPM_HOST:/root/mmtop/RPMS/*/*.rpm" "$RPM_HOST:/root/mmtop/SRPMS/*.rpm" build/rpm/
else
  for s in "$T"/top/SPECS/*.spec; do rpmbuild --define "_topdir $T/top" -ba "$s" >/dev/null; done
  cp "$T"/top/RPMS/*/*.rpm "$T"/top/SRPMS/*.rpm build/rpm/
fi
packaging/version-check.sh record
echo "== built:"; ls build/rpm | grep -- "-$V-1" | grep -v -e debug
