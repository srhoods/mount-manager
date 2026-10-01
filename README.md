# Mount Manager

Centrally managed network mounts for Linux fleets. A small agent on each host polls a central server every few
minutes and adds, updates or removes mounts exactly as defined, then reports back what happened. It replaces
"push mounts with a nightly configuration-management run" with continuous, verified convergence and one place to
see the state of every host.

- **Mounts and groups.** A mount defines what to mount and where. Groups attach mounts to hosts, either by explicit
  membership or by a regular expression on the hostname. When groups overlap, an explicit priority decides.
- **Safe updates.** Changing a mount option means unmount, then remount. If the unmount fails (open files), the old
  mount stays in place, the host is shown as *pending* with the reason, and the agent keeps retrying.
- **Self-enrolling hosts.** A new host presents a one-time token, receives a client certificate, and joins every
  group its name matches without any manual step.
- **Auditable.** Every mount action is written to the host's syslog and to a central, indefinitely retained audit log.
- **Least privilege.** The agent runs as a service account and mounts through tightly scoped sudo rules. Agent-side
  allow-lists (paths and filesystem types) limit what the server can ask it to do.
- **Resilient.** Agents keep working from their last known state if the server is unreachable, including across reboots.
- **Web UI, CLI and API.** A web UI and the `mmctl` command line share one REST API; enrolment tokens can be listed and revoked. Sign in with local accounts or
  Active Directory / LDAPS, with `admin`, `operator` and `readonly` roles.
- **Encrypted end to end.** Agents talk to the server over mutual TLS, using certificates issued by the server's
  built-in CA. The web and agent listeners can present certificates from your own CA instead, reloaded without a restart.

Supported today: **NFS** (`nfs`, `nfs4`) and **Weka** (`wekafs`). Client drivers, such as the Weka client, are
installed separately. Local filesystems are deliberately out of scope. Target platform: Rocky Linux 9+.

## How it fits together

```
   admins (web UI, mmctl)                                       managed hosts (mmd agent)
            │ HTTPS :8444                                              │ mTLS :8443
            ▼                                                          ▼
        ┌────────────────────────────────────────────────────────────────┐        ┌────────────┐
        │ mountmgr-server  (Go)                                          │ ─────▶ │ PostgreSQL │
        │  admin API + web UI · agent API · built-in CA · LDAPS sign-in  │        └────────────┘
        └────────────────────────────────────────────────────────────────┘
```

| Component | Language | Package | Runs on |
|-----------|----------|---------|---------|
| `mmserver` | Go | `mountmgr-server` | the server |
| `mmctl` | Go | `mountmgr-cli` | admin workstations, the server |
| `mmd` | C (libcurl, OpenSSL) | `mountmgr-agent` | every managed host |
| Web UI | React + TypeScript | embedded in `mmserver` | — |

The design assumes thousands of hosts polling every 5 minutes (about 5,000 initially), which is a few requests per
second. See [DESIGN.md](DESIGN.md) for the design decisions and phasing.

## Quick start

The full procedure, with production notes and troubleshooting, is in **[docs/INSTALL.md](docs/INSTALL.md)**. In outline:

1. **Database.** Install PostgreSQL, create a `mountmgr` role and database, and allow the server to connect.
2. **Server.** Install `mountmgr-server`, set `MM_DSN` and `MM_NAMES` in `/etc/mountmgr-server/server.env`, create the
   first admin, and start the service:

   ```bash
   runuser -u mmserver -- mmserver -init-admin '<password>'    # with server.env sourced
   systemctl enable --now mountmgr-server
   ```

3. **Sign in.** Fetch and pin the server's CA (`curl -sk https://<server>:8443/v1/ca`), then use the web UI at
   `https://<server>:8444/` or `mmctl login https://<server>:8444 admin --ca ca.pem`.
4. **Define mounts.**

   ```bash
   mmctl mount set data nfs01:/export/data /mnt/data --type nfs --opts rw,_netdev,hard
   mmctl group set render-farm --priority 100 --regex '^render-\d+\.example\.com$'
   mmctl group add-template <group-id> <mount-id>
   ```

5. **Enrol a host.** Create a token (`mmctl token create --hours 48 --uses 50`), install `mountmgr-agent`, set
   `server=` and `ca_sha256=` in `/etc/mountmgr/agent.conf`, put the token in `/var/lib/mountmgr/enroll.token`, and
   run `systemctl enable --now mountmgr-agent`.

Check the result with `mmctl host ls`, `mmctl host show <id>` and `mmctl audit`.

## Documentation

| Document | Contents |
|----------|----------|
| [docs/INSTALL.md](docs/INSTALL.md) | Postgres, server, LDAPS, defining mounts, bootstrapping agents, troubleshooting |
| [DESIGN.md](DESIGN.md) | Architecture, data model, agent behaviour, decisions and phasing |
| [CHANGELOG.md](CHANGELOG.md) | Release notes and upgrade notes |

## Building and testing

Build machine: Go 1.26.0+, Node.js 18+ (not Rocky 9's default v16; see [docs/INSTALL.md](docs/INSTALL.md#appendix-a-building-the-packages)),
and an EL9 host with `rpm-build` for the RPMs.

```bash
RPM_HOST=root@el9-builder packaging/build-rpms.sh     # builds agent, server and CLI RPMs into build/rpm/
make -C agent check                                   # agent mount-logic tests (no root or NFS needed)
cd server && go test ./...                            # add MM_TEST_DSN=postgres://… for the database-backed tests
cd web && npm ci && npm run build                     # builds the UI into the server's embed directory
```

- The agent's tests run its real reconcile code against fake `mount`, `umount` and `sudo` commands. They cover busy
  unmounts, retries, failures, timeouts, adopting existing mounts, allow-lists and nested mountpoints.
- The server's database tests cover group and priority resolution and role enforcement. Each run uses a throwaway
  schema.
- **Versioning:** one `VERSION` file drives all three packages. The build refuses to package changed sources under an
  unchanged version, and requires a matching `CHANGELOG.md` entry. Bump the minor version for features and the patch
  version for fixes.

## Status

Version 0.4.x. Working and tested on Rocky Linux 9: enrolment and mTLS, mount and group resolution, safe
remounts, adoption of existing mounts, LDAPS sign-in, the web UI and RPM packaging.

Known limitations and planned work:

- The server runs as a single instance. High availability and multi-site operation are designed for (the store is
  PostgreSQL) but not implemented.
- Changes apply to all affected hosts at once. Canary and batch rollouts are not implemented.
- Mounts are made by the agent after the network is up and are not in `/etc/fstab`, so `mmd` must be running after a
  reboot, and services that need a mount at boot should tolerate it appearing a moment after `remote-fs.target`.
- `wekafs` support is verified with fake mount commands and the failure path only, not against a real Weka cluster.

## Security

The server holds the CA private key in PostgreSQL, so protect the database and its backups accordingly. Enrolment tokens
are single-purpose credentials: limit their lifetime and number of uses, and pin the CA fingerprint on agents
(`ca_sha256`). See the security notes in [docs/INSTALL.md](docs/INSTALL.md).

## License

MIT. See [LICENSE](LICENSE).
