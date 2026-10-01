# Mount Manager — Installation Guide

This guide covers three things, in order:

1. [Postgres](#1-postgres) — the data store
2. [Mount Manager server](#2-mount-manager-server) — API, built-in CA, web UI
3. [Bootstrapping a new host](#3-bootstrapping-a-new-host-agent) — installing and enrolling the agent

It applies to version 0.2.x on Rocky Linux 9+. High availability and multi-site are not covered (not implemented yet).

## Architecture in one page

```
                      HTTPS (browser, mmctl)                 mTLS (agents)
  admins ──────────────────────────────┐            ┌──────────────────────── hosts (mmd)
                                       ▼            ▼
                              ┌──────────────────────────┐        SQL        ┌────────────┐
                              │ mountmgr-server          │ ────────────────▶ │ PostgreSQL │
                              │  :8444 admin API + web UI│                   └────────────┘
                              │  :8443 agent API (mTLS)  │
                              │  built-in CA             │
                              └──────────────────────────┘
```

| Port | Host | Purpose | Who connects |
|------|------|---------|--------------|
| 5432/tcp | Postgres | database | the server only |
| 8443/tcp | server | agent API (mTLS after enrolment) | every managed host |
| 8444/tcp | server | web UI and REST API (HTTPS, sign-in required) | admins, `mmctl` |

Packages (build them with [Appendix A](#appendix-a-building-the-packages)):

| RPM | Install on | Contains |
|-----|-----------|----------|
| `mountmgr-server` | the server | `mmserver`, systemd unit, `server.env`, `ldap.json.example` |
| `mountmgr-cli` | admin workstations / the server | `mmctl` |
| `mountmgr-agent` | every managed host | `mmd`, systemd unit, `agent.conf`, sudoers rules |

Sizing: the design target is about 5,000 agents polling every 5 minutes, which is a few requests per second. A
small VM (2 vCPU, 4 GB) is ample for the server; Postgres has the same modest needs.

---

## 1. Postgres

Developed and tested against PostgreSQL 16 (the Rocky 9 AppStream module), which these steps use. Other recent versions should work but are untested.

### 1.1 Install and initialise

```bash
dnf -y module enable postgresql:16
dnf -y install postgresql-server
postgresql-setup --initdb
systemctl enable --now postgresql
```

### 1.2 Create the database and role

Generate a strong password and keep it; the server needs it in section 2.

```bash
PW=$(openssl rand -hex 16); echo "$PW"       # record this securely
sudo -u postgres psql <<EOF
CREATE ROLE mountmgr LOGIN PASSWORD '$PW';
CREATE DATABASE mountmgr OWNER mountmgr;
EOF
```

The server creates and upgrades its own tables at start-up (the schema is additive and idempotent), so the role
only needs to own the database.

### 1.3 Allow the server to connect

Edit `/var/lib/pgsql/data/postgresql.conf`:

```
listen_addresses = '*'          # or the specific interface address
```

Edit `/var/lib/pgsql/data/pg_hba.conf` and allow **only the server's address** (repeat per server when you add more):

```
host  mountmgr  mountmgr  <server-ip>/32  scram-sha-256
```

```bash
systemctl restart postgresql
firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=<server-ip>/32 port port=5432 protocol=tcp accept'
firewall-cmd --reload             # only if firewalld is running
```

Verify from the server host: `psql "postgres://mountmgr:$PW@<pg-host>:5432/mountmgr" -c 'select 1'`.

### 1.4 Production notes

- **Encrypt the connection.** Enable TLS in Postgres (`ssl = on` with a certificate) and use
  `?sslmode=verify-full` in the DSN. The examples below omit it only because the lab network is isolated.
- **Back it up — and protect the backups.** The database holds the configuration, the audit history (kept
  indefinitely), sessions, **and the CA private key and server key** (table `kv`). Anyone who can read a backup can
  mint agent certificates. Use `pg_dump`/`pg_basebackup`, encrypt the backups, and restrict who can read them.
- **Disk growth.** The `audit` table grows for ever by design. At 5,000 hosts a quiet fleet adds few rows;
  a change storm adds a few per host. Plan partitioning or archival before it matters.

---

## 2. Mount Manager server

### 2.1 Install

```bash
dnf -y install ./mountmgr-server-<version>.el9.x86_64.rpm ./mountmgr-cli-<version>.el9.x86_64.rpm
```

This creates the `mmserver` system user and installs `/usr/sbin/mmserver`, the `mountmgr-server` unit and
`/etc/mountmgr-server/`. The service is **not** started yet.

### 2.2 Configure `/etc/mountmgr-server/server.env`

The file is mode `0640 root:mmserver` because it holds the database password.

```ini
MM_DSN=postgres://mountmgr:<PASSWORD>@<pg-host>:5432/mountmgr?sslmode=verify-full
# Every DNS name and IP that agents or admins will use to reach this server. These become the
# subject alternative names of the server certificate - a name that is not listed here will fail TLS.
MM_NAMES=mm01.example.com,10.20.30.40
MM_AGENT_LISTEN=:8443
MM_ADMIN_LISTEN=:8444
#MM_LDAP_CONFIG=/etc/mountmgr-server/ldap.json     # see 2.6
```

Use a DNS name that you can keep for the life of the deployment (for example a CNAME or load-balancer name):
agents are configured with it.

### 2.3 Create the first administrator and start the service

Create the local `admin` account **before** starting the service (this also creates the database tables). Pick a
strong password; this account is your break-glass login if the directory is unavailable.

```bash
set -a; . /etc/mountmgr-server/server.env; set +a
runuser -u mmserver -- mmserver -init-admin '<a strong password>'
systemctl enable --now mountmgr-server
firewall-cmd --permanent --add-port=8443/tcp --add-port=8444/tcp && firewall-cmd --reload   # if firewalld is running
```

On first start the server generates its private CA, stores it in Postgres, and issues its server certificate
(also stored, so it is stable across restarts; it is reissued only if `MM_NAMES` changes or it is close to expiry).

Check it:

```bash
systemctl status mountmgr-server
journalctl -u mountmgr-server -n 20        # expect: "generated new CA", "mmserver <version>: agent :8443 admin :8444"
```

To reset the admin password later, re-run the `-init-admin` command.

### 2.4 Trust the CA, then sign in

The server's HTTPS certificate is issued by the built-in **Mount Manager CA**, so clients must trust that CA.

```bash
curl -sk https://mm01.example.com:8443/v1/ca > mountmgr-ca.pem
sha256sum mountmgr-ca.pem          # this fingerprint is used to pin agents in section 3; record it
```

Do this once on a machine you trust (ideally the server itself, `https://localhost:8443/v1/ca`) and compare the
fingerprint elsewhere.

- **CLI:** `mmctl login https://mm01.example.com:8444 admin --ca mountmgr-ca.pem` (the password is read from
  `MM_PASSWORD` or standard input). The session is stored in `~/.mmctl.json` (mode 0600). `--insecure` skips
  certificate checks and is for labs only.
- **Web UI:** browse to `https://mm01.example.com:8444/`. Import `mountmgr-ca.pem` as a trusted authority in the
  browser or OS, otherwise the browser will warn. Sessions last 12 hours; "Keep me signed in" stores the session
  token in the browser's local storage, so untick it on shared machines.

> Prefer your corporate CA for the web UI? See [2.5](#25-optional-certificates-from-your-own-ca): browsers then need
> no extra trust configuration.

### 2.5 Optional: certificates from your own CA

By default both listeners present a certificate issued by the built-in CA. Either listener can instead present a
certificate from your own (corporate or public) CA:

| Listener | Setting | Used by |
|----------|---------|---------|
| 8444 web UI and API | `MM_ADMIN_TLS_CERT` / `MM_ADMIN_TLS_KEY` | browsers, `mmctl` |
| 8443 agent API | `MM_AGENT_TLS_CERT` / `MM_AGENT_TLS_KEY` | agents |
| both | `MM_TLS_CERT` / `MM_TLS_KEY` | whichever listener has no specific setting |

1. **Get the certificate.** It must cover every name clients use to reach the server (for example `mm01.example.com`).
   Provide a PEM file with the server certificate first, followed by any intermediate certificates, and the private
   key as an **unencrypted** PEM file.
2. **Install the files** where the service account can read them:

   ```bash
   install -d -m 0750 -o root -g mmserver /etc/mountmgr-server/tls
   install -m 0644 -o root -g mmserver mm01-chain.pem /etc/mountmgr-server/tls/server.crt
   install -m 0640 -o root -g mmserver mm01.key       /etc/mountmgr-server/tls/server.key
   ```

3. **Point the server at them** in `server.env` (for example `MM_TLS_CERT=/etc/mountmgr-server/tls/server.crt` and
   `MM_TLS_KEY=/etc/mountmgr-server/tls/server.key`) and run `systemctl restart mountmgr-server`. The log confirms the
   certificate and warns if it expires within 30 days or does not cover a name in `MM_NAMES`:

   ```
   admin listener: external certificate subject="mm01.example.com" issuer="Corp Issuing CA" names=[mm01.example.com] expires=2027-08-01
   ```

- **Renewals need no restart.** Replace the two files; the server notices within about ten seconds and logs
  `loaded new certificate`. A certificate that is expired, unreadable or does not match its key is rejected and the
  previous one keeps being served (the log says so).
- **Web UI and CLI:** browsers that already trust your CA need nothing further, and `mmctl login https://mm01.example.com:8444 admin`
  works without `--ca` (it uses the system trust store; add `--ca bundle.pem` otherwise).
- **Agents and the agent listener (8443).** Agents verify the server with the built-in CA unless told otherwise.
  - If you leave 8443 on the built-in certificate, nothing changes for agents. This is the simplest option.
  - If 8443 uses an external certificate, agents must trust that CA: set `server_ca_file=` in `/etc/mountmgr/agent.conf` to the
    CA bundle that signed it, or `server_ca_file=system` to use the operating system's trust store
    (`/etc/pki/tls/certs/ca-bundle.crt`), and restart the agent. New hosts set it before enrolling (no
    `ca_sha256` is needed, because trust comes from your CA).
  - **Migration is safe in either order.** An agent with `server_ca_file` trusts that CA *and* the built-in CA it already
    holds, so you can roll the setting out to the fleet at your own pace and switch the server when every agent has it.
    An agent that has not been updated cannot connect to a server presenting the new certificate; it keeps its mounts
    from cached state and recovers as soon as it is configured. The combined trust file is kept in
    `/var/lib/mountmgr/trust.pem` and rebuilt when the bundle changes.
- **Client certificates** (what agents authenticate with) are always issued by the built-in CA, whatever the server
  certificates are. The built-in CA therefore stays in the database and its backups remain sensitive.
- LDAPS trust is separate (`ca_file` in `ldap.json`).

### 2.6 Optional: Active Directory / LDAPS sign-in

Local accounts always work. To add directory sign-in:

```bash
cd /etc/mountmgr-server
cp ldap.json.example ldap.json        # edit: url, bind_dn, user_base, user_filter, role_map ...
install -m 0640 -o root -g mmserver /path/to/directory-ca.pem ldap-ca.pem
printf '%s\n' 'service-account-password' > ldap-bind.password
chown root:mmserver ldap.json ldap-bind.password; chmod 0640 ldap.json ldap-bind.password
```

- Only `ldaps://` is accepted; the directory certificate is verified against `ca_file`.
- `role_map` maps **direct** group membership (nested groups are not evaluated) to `admin`, `operator` or
  `readonly`; keys are full group DNs or bare CNs, the highest matching role wins, and users with no mapped group
  are refused.
- **Where `ldap-ca.pem` comes from.** It is the certificate of the CA that signed your domain controllers' LDAPS
  certificates, in PEM (Base-64) form. It is not produced by Mount Manager. Ask your PKI or AD team for the root (and any
  intermediate) CA certificates, or export them from a domain-joined Windows machine (`certmgr.msc` → Trusted Root
  Certification Authorities → Export → "Base-64 encoded X.509"). Several certificates can be concatenated in one file, and a
  binary `.cer` converts with `openssl x509 -inform der -in corp-root.cer -out ldap-ca.pem`. As a last resort, read it from the
  domain controller with `openssl s_client -connect dc1.corp.example.com:636 -showcerts </dev/null` and take the *CA*
  certificates of the chain (not the first, the server's own), after checking the fingerprint with your PKI team. If your
  corporate CA is already in the operating system's trust store, omit `ca_file` and the system roots are used.
- **Test before enabling** (no database needed). It prompts for the user's password without echoing it, then reports each
  step, so a slow or failing directory is visible rather than silent. It prints the DN, groups and resolved role:

  ```bash
  set -a; . /etc/mountmgr-server/server.env; set +a
  runuser -u mmserver -- mmserver -ldap-config /etc/mountmgr-server/ldap.json -ldap-check jsmith
  ```

  ```
  Password for jsmith:
    - connecting to ldaps://dc1.corp.example.com:636 (TLS verified against /etc/mountmgr-server/ldap-ca.pem, 10s timeout per step)
    - connected; TLS handshake and certificate verification succeeded
    - binding as service account CN=svc-mountmgr,...
    - searching DC=corp,DC=example,DC=com with filter (&(objectClass=user)(sAMAccountName=jsmith))
    - found CN=John Smith,OU=Users,...
    - verifying the password by binding as CN=John Smith,OU=Users,...
  Role:   admin
  OK
  ```

  The path after `-ldap-config` is required. For scripts, set `MM_LDAP_TEST_PASSWORD` or pipe the password in on standard input.
  Where it stops tells you what to fix: no `connecting…` result means DNS, routing or a firewall on port 636; a
  certificate error means `ca_file` or the name in `url` (use `server_name` if the certificate carries a different name);
  `service bind failed` means `bind_dn` or its password; `matched 0 entries` means `user_base` or `user_filter`; `not a member
  of any group mapped to a role` means `role_map`.

- Enable by uncommenting `MM_LDAP_CONFIG=/etc/mountmgr-server/ldap.json` in `server.env` and running
  `systemctl restart mountmgr-server`. The log shows `directory login enabled`.

Roles: `readonly` can view everything; `operator` can also change mounts, groups, memberships and hosts;
`admin` can also create enrolment tokens. Five failed sign-ins for one user from one address block further
attempts for five minutes. Every sign-in, denial and failure is written to the audit log.

### 2.7 Define what to mount

Mounts are defined as **mounts** (one per filesystem to mount), applied to hosts through **groups**. (Older releases called a mount a
"template"; the `mmctl template` command and the REST paths under `/api/templates` still work.)

```bash
# a mount = one filesystem (types: nfs, nfs4, wekafs)
mmctl mount set data  nfs01:/export/data   /mnt/data --type nfs  --opts rw,_netdev,hard
mmctl mount set fast  weka01/fs1           /sqpc     --type wekafs --opts rw
mmctl mount clone data data-archive         # copy a mount under a new name, then change what differs with 'mount set'

# a group = a set of mounts + members; membership is by explicit host and/or hostname regex
mmctl group set render-farm --priority 100 --regex '^render-\d+\.example\.com$'
mmctl group ls                                 # note the group id and mount ids
mmctl group add-template <group-id> <mount-id>
```

- **Priority:** if a host is in several groups that define the *same mountpoint*, the group with the higher
  priority wins. Give overlapping groups different priorities.
- **Regex:** unanchored Go (RE2) syntax matched against the host's name as the agent reports it (normally the
  FQDN). Anchor with `^…$` unless you mean a substring. New hosts that match join automatically the first time
  they poll.
- **Changing a mount** (say, a mount option) rolls out to every host using it on its next poll (within about
  5 minutes): the agent unmounts and remounts. A mount that is busy is reported as *pending* and retried.
- The web UI (Mounts and Groups pages) does the same and previews which hosts a regex matches. **Clone** on the Mounts page
  copies a mount under a new name (it is not added to any group; use Edit to change what differs). Mount options show when you hover over a
  row. Cloning never overwrites: a name that is already taken is refused.
- Where mounts are allowed is enforced **on each host** (see 3.4), not just by the server.

### 2.8 Upgrades

```bash
dnf -y install ./mountmgr-server-<new>.el9.x86_64.rpm ./mountmgr-cli-<new>.el9.x86_64.rpm   # upgrades in place; restarts the service
```

Configuration files are preserved. When the packaged default of a config file changes you may find a
`server.env.rpmnew` beside it; compare and merge by hand. The schema is upgraded automatically at start-up (version 0.3.0 adds columns to the enrolment token table; existing tokens keep working).
Read `CHANGELOG.md` first for anything marked *Upgrade notes*.

---

## 3. Bootstrapping a new host (agent)

What happens: the agent downloads the CA, proves it holds a one-time **enrolment token**, sends a certificate
signing request, and receives a client certificate (30 days, renewed automatically over mTLS when fewer than 7
days remain). From then on it polls every 5 minutes using that certificate, joins the groups its name matches,
and mounts what they define.

### 3.1 Prerequisites

- Rocky 9+ (`nfs-utils` and `sudo` are installed as RPM dependencies).
- The host can reach the server on TCP **8443**, and its NFS/Weka servers by name.
- The host's name (`hostname`) is what appears in the UI and what group regexes match; use the FQDN the same way
  everywhere. Override with `hostname=` in `agent.conf` if needed.
- For `wekafs` mounts, the Weka client is installed separately (by Ansible today).

### 3.2 Create an enrolment token (on the server, or in the UI: Enrolment → Generate token)

Administrator role required.

```bash
mmctl token create --note "render farm batch 3" --hours 48 --uses 50
```

The token is printed **once** and stored only as a hash. Limit both its lifetime and its number of uses to what
the batch needs; a leaked token lets its holder enrol a machine with any hostname until it runs out.

Track and withdraw tokens from the **Enrolment** page, which lists the active tokens with their uses remaining, the
time left, who created them and when they expire, or from the CLI:

```bash
mmctl token ls               # active tokens: id, status, note, uses (left/total), time left
mmctl token ls --all         # also expired, used-up and revoked tokens
mmctl token revoke <id>      # withdraw a token that can still be used (e.g. it leaked, or the batch is cancelled)
```

Revoking stops further enrolments with that token immediately; hosts that already enrolled keep working. Every
creation, revocation and refused enrolment is written to the audit log, and each enrolment names the token used.

### 3.3 Install and enrol the host

```bash
dnf -y install ./mountmgr-agent-<version>.el9.x86_64.rpm

# point the agent at the server; pin the CA fingerprint from step 2.4
sed -i 's#^server=.*#server=https://mm01.example.com:8443#' /etc/mountmgr/agent.conf
echo 'ca_sha256=<fingerprint from 2.4>' >> /etc/mountmgr/agent.conf

# drop the token where the agent looks for it (0600, owned by the service account)
install -o mountmgr -g mountmgr -m 0600 /dev/null /var/lib/mountmgr/enroll.token
echo '<token>' > /var/lib/mountmgr/enroll.token

systemctl enable --now mountmgr-agent
```

> If the server's agent listener uses a certificate from your own CA (see [2.5](#25-optional-certificates-from-your-own-ca)),
> set `server_ca_file=` (your CA bundle, or `system`) instead of `ca_sha256`.

**Pinning matters.** Without `ca_sha256` the agent trusts whatever CA the server presents on first contact and
only logs a warning (`trust-on-first-use`). With it, the agent refuses to continue if the fingerprint differs.
The agent deletes the token after a successful enrolment.

For a fleet, bake the same three steps into your existing automation (Ansible, kickstart, image build): install
the RPM, template `agent.conf`, drop the token file, enable the service.

### 3.4 Verify

On the host:

```bash
systemctl status mountmgr-agent
grep 'mountmgr\[' /var/log/messages | tail        # enrol result=ok, mount ... result=ok
findmnt -t nfs,nfs4                                 # (or -t wekafs)
```

On the server (or in the UI Hosts page):

```bash
mmctl host ls                    # STATUS: in-sync; PROBLEMS: empty
mmctl host show <id>             # desired mounts vs actual state
mmctl audit --host <hostname>    # enrol / mount / umount history
```

The Hosts page is paginated (25, 50, 100 or 250 rows per page) and filters by name and state on the server, so it stays
quick with thousands of hosts. States are `IN SYNC`, `PENDING`, `OFFLINE` (no contact for 15 minutes) and `UNKNOWN`.

Expect the first mounts within seconds of the service starting. If the host matches no group it enrols but has
nothing to mount ("No mounts are assigned to this host" in the UI).

### 3.5 What the agent is allowed to do

- Runs as the `mountmgr` service account, not root. `/etc/sudoers.d/mountmgr` (package-owned) permits only
  `mount -t nfs|nfs4|wekafs`, `umount` under `/mnt`, `/data`, `/sqpc`, and `mkdir -p` in the same places.
- **Agent-side allow-lists** in `/etc/mountmgr/agent.conf` limit what the server can make it do, regardless of
  what is configured centrally. Defaults:

  ```ini
  allowed_mount_prefixes=/mnt,/data,/sqpc   # mounts below these paths
  allowed_mount_exact=/sqpc                 # mounts at exactly these paths
  allowed_fstypes=nfs,nfs4,wekafs
  ```

  To allow another location, extend **both** `agent.conf` and the sudoers rules (add a separate file in
  `/etc/sudoers.d/`; the packaged one is replaced on upgrade). A refused mount shows in the UI as failed with the
  reason.
- Pre-existing mounts (for example from Ansible) at the same mountpoint are **adopted, not disturbed**, when
  the source matches (NFS) or the type matches (wekafs); otherwise the agent replaces them to match the mount definition.
  It never unmounts anything it did not mount itself.
- Every action is logged to syslog (`/var/log/messages`) as `action=… mountpoint=… result=…` and reported to
  the server's audit log.

### 3.6 Behaviour worth knowing

- **Slow first Weka mount:** the first `wekafs` mount on a host compiles the client driver and starts the Weka container, so
  the agent allows `mount_timeout_wekafs` (default 120 seconds) for it, against `mount_timeout` (default 60) for NFS.
  Both are set in `agent.conf`. If a mount still times out, the root mount process keeps running in the background, so the
  next cycle finds it mounted and adopts it.
- **Changing a mount that is in use:** the agent unmounts first, then remounts. If the unmount fails (open
  files), the old mount is left in place, the host shows **Pending** with the reason, and the agent retries
  every `retry_interval` (default 900 s).
- **Server unreachable:** the agent keeps the last desired state on disk (`/var/lib/mountmgr/desired.tsv`) and
  keeps reconciling from it, including after a reboot. A host that has never enrolled needs the server once.
- **Mounts are not in `/etc/fstab`;** they are created by the agent after the network is up, so services that
  need them at boot should tolerate the mounts appearing a moment after `remote-fs.target`, and `mmd` being
  down means no mounts after a reboot.
- **Removing a host:** `mmctl host rm <id>` (or the UI). Its certificate stops working; it must enrol again with a
  new token to come back. Uninstalling the RPM does not unmount anything.

### 3.7 Troubleshooting

| Symptom (in `/var/log/messages`, `action=…`) | Likely cause and fix |
|---|---|
| `enroll result=failed error="no enrol token at …"` | The token file is missing or not readable by `mountmgr`; see 3.3. |
| `enroll result=failed http=403` | Token wrong, expired or out of uses. Create a new one. |
| `fetch-ca … fingerprint mismatch` | `ca_sha256` does not match the server's CA. Wrong server, or the CA was regenerated: re-check with `curl -sk https://<server>:8443/v1/ca \| sha256sum`. Never "fix" this by deleting the pin without finding out why. |
| `fetch-ca result=failed http=-1`, `poll result=failed http=-1` | Cannot reach the server: DNS, firewall (8443), or the server is down. Mounts continue from the cached state. |
| `poll … http=403` after it worked | The host was removed on the server, or its certificate was superseded. Re-enrol with a new token (stop the agent, delete `/var/lib/mountmgr/client.*`, add the token). |
| `poll result=failed http=-1` right after the server was given an external certificate | The agent does not trust the new CA. Set `server_ca_file` (see 2.5) and restart the agent. |
| TLS error mentioning the server name | The name in `agent.conf` is not in the server's `MM_NAMES`. Add it and restart the server. |
| `mount … result=refused reason="not an allowed mountpoint"` / `"fstype not allowed"` | Outside the agent allow-lists (3.5). |
| `mount … result=failed error="…"` | The mount command's own error, e.g. name resolution, `Connection timed out`, `unknown filesystem type 'wekafs'` (driver not installed). |
| `umount … result=busy` / host shows **Pending** | Something has files open on the old mount; it is retried automatically. `fuser -vm <mountpoint>` finds the culprit. |
| `sudo: a password is required` | `/etc/sudoers.d/mountmgr` is missing or altered, or the command is outside its rules (for example a mountpoint the sudoers file does not cover). |
| Host enrolled but shows no mounts | It matches no group. Check the regex against the exact hostname (`mmctl host ls`), and that the group has mounts. |

Run the agent once in the foreground for detailed output: `systemctl stop mountmgr-agent; runuser -u mountmgr -- mmd -f -1`.

---

## Appendix A: Building the packages

Build machine requirements:

- **Go 1.26.0 or newer** (the module's minimum).
- **Node.js 18 or newer, with npm**, for the web UI. On Rocky 9 the default `nodejs` package is **v16, which is too
  old** and fails with `TypeError: crypto.getRandomValues is not a function`. Install a supported stream instead:

  ```bash
  dnf -y module reset nodejs && dnf -y module enable nodejs:20 && dnf -y distro-sync nodejs npm
  node -v          # v20.x
  ```

- An EL9 host with `rpm-build`, `gcc`, `make`, `libcurl-devel` and `openssl-devel` for `rpmbuild` (can be the same
  machine).

The build script checks the Node and Go versions first and stops with the fix if either is too old.

```bash
RPM_HOST=root@el9-builder packaging/build-rpms.sh     # rpmbuild runs over ssh on that host
# or, on an EL9 machine with the toolchain:
packaging/build-rpms.sh
```

The script checks the version rules, runs the Go tests and the web build, builds all three RPMs at the version in
`VERSION`, and runs the agent's mount-logic test suite during the agent build. Output: `build/rpm/`.
It refuses to build if sources changed without a `VERSION` bump and a `CHANGELOG.md` entry.

To run the database-backed server tests as well, point `MM_TEST_DSN` at any Postgres (each run uses a throwaway
schema): `MM_TEST_DSN='postgres://…' go test ./...` from `server/`.

## Appendix B: Quick reference

| Task | Command |
|------|---------|
| Server logs | `journalctl -u mountmgr-server -f` |
| Agent logs | `grep 'mountmgr\[' /var/log/messages` |
| Versions | `mmserver -version`, `mmctl version`, `mmd -V` |
| Reset the admin password | `runuser -u mmserver -- mmserver -init-admin '<new>'` (with `server.env` sourced) |
| Test directory login | `mmserver -ldap-config … -ldap-check <user>` |
| List hosts / one host | `mmctl host ls` / `mmctl host show <id>` |
| Audit trail | `mmctl audit [--host name] [--limit N]` |
