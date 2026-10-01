# Changelog

Versioning: one `VERSION` for the agent, server and CLI RPMs. Bump the **minor** version for features and the
**patch** version for fixes; the build (`packaging/build-rpms.sh`) refuses to package changed sources under an
unchanged version, and requires a `## <version>` entry here. The RPM *Release* only changes for packaging-only
respins (it is 1 for every new version).

## 0.6.0
### Added
- **Search in the group editor.** The Mounts checklist has a search box (name, source, mountpoint or type), a "Selected only"
  toggle and a selected count. Filtering never drops a ticked mount, and the list shows the mount's type, mountpoint and source.
### Changed
- Long checklists in the group editor (mounts and static members) render at most 200 rows, with a prompt to narrow the search,
  so the editor stays responsive with thousands of mounts or hosts.

## 0.5.0
### Added
- **Mounts page: pagination and search.** The same 25 / 50 / 100 / 250 rows-per-page pager as the Hosts page, plus separate
  search boxes for name, source and mountpoint and a type drop-down (nfs, nfs4, wekafs). The filters combine, the page resets
  when they change, and "Clear filters" resets them. Searching and paging happen on the server, so the page stays quick with
  thousands of mounts. `GET /api/templates` accepts `name`, `source`, `mountpoint`, `type`, `limit` and `offset` and returns
  `X-Total-Count` (without parameters it still returns everything, so existing callers are unaffected).
  `mmctl mount ls` gains `--name`, `--source`, `--mountpoint`, `--type`, `--limit` and `--offset`.
### Changed
- Mounts are listed in case-insensitive name order regardless of the database's collation.

## 0.4.0
### Added
- **Clone a mount.** Each row on the Mounts page has a Clone button that asks for the name of the new mount and copies
  the type, source, mountpoint and options. The copy is not added to any group and is changed through the usual Edit.
  Cloning never overwrites: an existing name is refused (`POST /api/templates/{id}/clone`, `mmctl mount clone`). Anyone who
  can edit mounts (administrators and operators) can clone.
- Separate agent timeout for `wekafs`: `mount_timeout_wekafs` (default **120 s**; `mount_timeout` stays 60 s for NFS). The
  first Weka mount on a host compiles the client driver and starts the Weka container, which previously timed out.
### Changed
- **Templates are now called Mounts** in the web UI (navigation, Groups, dialogs), the CLI (`mmctl mount ...`; `mmctl template ...`
  still works) and the documentation. The REST API paths (`/api/templates`) and stored audit action names are unchanged.
  Old `#/templates` bookmarks open the Mounts page.
- The Mounts page no longer has an Options column; the options appear when hovering over (or focusing) a mount.
- Pages use the full width of the window instead of stopping at 1,500 px, which suits high-resolution monitors; wide
  dialogs are larger too.
### Fixed
- Creating a new mount with the name of an existing one silently replaced the existing mount. The New dialog now refuses it
  (Edit changes a mount, Clone copies it).

## 0.3.4
### Fixed
- `mmserver -ldap-check` appeared to hang: with no `MM_LDAP_TEST_PASSWORD` it silently waited for a password on standard
  input. It now prompts for the password (without echo) on a terminal, and prints each step it takes (connect, TLS, service
  bind, user search, group search, password check), so a slow or failing directory is visible and each step is bounded by
  `timeout_seconds`.
- `mmserver -ldap-config -ldap-check <user>` (path left out) made the flag parser take `-ldap-check` as the config path and
  start the real server instead of running the check. A flag given where a path is expected is now rejected with usage.
- Install guide: where `ldap-ca.pem` comes from, and how to read the check output.

## 0.3.3
### Fixed
- Web UI: the host dialog now reloads its data if a different host is opened while it is showing (it could keep
  displaying the previous host's mounts and events until the next refresh).
- Web UI: hostnames in the dashboard's "Needs attention" list no longer wrap.

## 0.3.2
### Fixed
- Web UI: opening a host that has no mounts assigned (for example a newly enrolled host that matches no group) blanked
  the whole page. The API returned `null` instead of an empty list and the dialog iterated over it. The API now returns
  empty lists, the UI tolerates `null`, and any future render error shows a message with a reload button instead of a
  blank page. (This affected earlier releases too.)
- Web UI: very long pages (250 rows per page) are now scrolled inside the table with a sticky header, so the pager is
  always on screen and the page stays a normal height.

## 0.3.1
### Fixed
- Agent: a new host configured with `server_ca_file` could not enrol, because the combined trust file was only built in
  the polling cycle, after enrolment. It is now built on first use.
- Database: enrolment tokens that predate 0.3.0 and were already used up now show a total of at least 1 use instead of 0.

## 0.3.0
### Added
- **Certificates from your own CA.** The web/API listener (8444) and the agent listener (8443) can each serve a
  certificate issued by an external CA (`MM_TLS_CERT`/`MM_TLS_KEY`, or `MM_ADMIN_TLS_*` / `MM_AGENT_TLS_*`). The
  files are re-read when they change, so renewals need no restart, and a bad replacement is rejected while the old
  certificate keeps being served. Startup warns about certificates that are close to expiry or do not cover
  `MM_NAMES`. Agents gain `server_ca_file` (a CA bundle, or `system`), trusted in addition to the built-in CA. Client
  certificates are still issued by the built-in CA.
- **Hosts page pagination** with 25 / 50 / 100 / 250 rows per page (remembered per browser) and first / previous /
  next / last navigation. Search and state filtering are now done by the server, so the page stays fast with
  thousands of hosts. `GET /api/hosts` accepts `q`, `state`, `limit`, `offset` and returns `X-Total-Count`;
  `GET /api/hosts/summary` provides the dashboard counts. `mmctl host ls` gains `--q`, `--state`, `--limit`, `--offset`.
- **Enrolment tokens can be listed and revoked.** The Enrolment page shows active tokens with uses remaining, time
  left, who created them and when they expire, and lets an administrator revoke a token that can still be used (an
  optional view includes expired, used-up and revoked tokens). `mmctl token ls [--all]` and `mmctl token revoke <id>`.
  Enrolments are audited with the token that was used, and refused attempts are recorded.
### Changed
- Host and token states are shown in upper case (`IN SYNC`, `PENDING`, `OFFLINE`, `UNKNOWN`, `ACTIVE`, ...).
- The host detail dialog uses the width of the window, so mount paths, sources, options and events no longer wrap.
### Upgrade notes
- The database schema is extended automatically at start-up (token id, creation time, creator, total uses and
  revocation). Existing tokens keep working; their "total uses" is initialised to the uses they had left.
- To move the **agent listener** to an external-CA certificate, roll out `server_ca_file` to the agents, then switch the
  server. An agent with `server_ca_file` trusts that CA and the built-in CA it already holds, so the order is not
  critical; agents without it cannot connect to a server that presents any other certificate (they keep working from
  cached state until configured).

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
