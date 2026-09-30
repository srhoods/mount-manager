# Changelog

Versioning: one `VERSION` for the agent, server and CLI RPMs. Bump the **minor** version for features and the
**patch** version for fixes; the build (`packaging/build-rpms.sh`) refuses to package changed sources under an
unchanged version, and requires a `## <version>` entry here. The RPM *Release* only changes for packaging-only
respins (it is 1 for every new version).

## 0.2.2
### Fixed
- Build: the web UI build failed with `crypto.getRandomValues is not a function` on Node.js 16, which is the default
  `nodejs` package on Rocky 9. Node.js 18 or newer is required. `web/package.json` now declares it (with
  `engine-strict`, so `npm ci` reports an unsupported Node clearly), and `packaging/build-rpms.sh` checks Node and Go
  up front and prints the fix.
- Build: `server/go.mod` demanded exactly Go 1.26.7 (`go 1.26.7`), so building with any earlier 1.26 release failed or
  tried to download a toolchain. The minimum is now Go 1.26.0. `go mod tidy` also corrected the `go-ldap` dependency
  from indirect to direct.

## 0.2.1
### Fixed
- Agent: mounts removed from the configuration are now unmounted before any new mount is made, deepest path first.
  Previously a new parent mount (e.g. `/sqpc`) made in the same cycle hid a nested child (`/sqpc/x`) that was being
  removed, so its unmount failed and was retried a cycle later.

## 0.2.0
### Added
- Operators can now change templates, groups, memberships and hosts (enrolment tokens remain admin-only).
- `wekafs` mount type (source `backend/filesystem`). The Weka client driver is still deployed by Ansible.
- `/sqpc` is a permitted mount location: mounts below it (`/sqpc/...`) and at `/sqpc` itself
  (`allowed_mount_prefixes` gains `/sqpc`; new `allowed_mount_exact=/sqpc`; sudoers updated).
- Agent `allowed_fstypes` allow-list (default `nfs,nfs4,wekafs`); the server no longer decides alone what gets mounted.
- Agent mount-logic test suite (`make -C agent check`, also run during the agent RPM build) and database-backed
  server tests for group/priority resolution and role enforcement (`MM_TEST_DSN`).
- `VERSION`, version embedded in binaries (`mmd -V`, `mmserver -version`, `mmctl version`), build-time version enforcement.
### Fixed
- Agent: per-mount status and retry timers were tied to the mount's position in the desired list, so adding or
  removing a mount could move one mount's failure/retry state onto another.
- Agent: adoption of pre-existing wekafs mounts no longer depends on the kernel reporting the same source string.
- Web UI, login, certificate handling and audit fixes from the 0.1.x builds are included.
### Upgrade notes
- `/etc/sudoers.d/mountmgr` is now replaced on upgrade (it is package-owned).
- `agent.conf` is preserved on upgrade. Existing installs that set `allowed_mount_prefixes=/mnt,/data` explicitly
  must add `/sqpc` (or delete that line to use the built-in defaults; a new `agent.conf.rpmnew` may be left for comparison)
  before mounting there.

## 0.1.x
Initial builds: server with built-in CA, agent, CLI, web UI, LDAPS login, RPM packaging.
