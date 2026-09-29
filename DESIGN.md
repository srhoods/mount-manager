# Mount Manager - Design (draft v0.1)

## Decisions
- Server: Go, stateless replicas behind an LB. Data store: PostgreSQL + Patroni (repository interface kept so the store can change).
- Agent `mmd`: C, single-threaded daemon, mTLS, polls every 5 min (+jitter). Runs as root or as `mountmgr` with sudoers rules.
- CLI `mmctl`: Go, thin REST client.
- Web UI: React/TypeScript SPA embedded in the Go server binary.
- PKI: built-in CA in the server; enrolment token + CSR; short-lived client certs, auto-renewed.
- Auth: local accounts (argon2) + LDAP/AD; RBAC (admin / operator / read-only).

## Data model
- Template: fstype (nfs, nfs4, vast, weka, fuse), source, mountpoint, options, version, variables ({{hostname}}, {{site}}).
- Group: static members + dynamic hostname-regex rules; linked to templates with priority.
- Host: cert identity, last seen, agent version, per-mount desired_rev / applied_rev / state (ok, pending, failed, busy) / last error.
- Audit event: append-only; admin actions and client mount actions.

## Agent behaviour
- `GET /v1/desired-state?rev=n` -> 304 if unchanged.
- Declarative, idempotent reconcile vs /proc/self/mountinfo; desired state cached on disk for offline operation.
- Update = umount then mount. EBUSY -> report `pending: busy`; server flags host "pending changes"; agent retries at configurable interval (default 15 min). No lazy/force unmount unless the template allows it.
- Mountpoint allow-list enforced client-side. Every action logged to syslog (key=value) and reported to server.

## Phasing
- MVP: single server + Postgres, agent, CLI, basic UI, mTLS enrolment, local + LDAP auth.
- Later: HA / multi-site (per-site read replicas), rollout controls (canary/batch).

## Resolved (round 2)
- Group conflicts: explicit integer priority per group; highest wins per mountpoint.
- Multi-site: single primary site acceptable for now (read replicas/HA later).
- Rollouts: "apply to all" for MVP; canary/batch/rollback deferred.
- Platform: Rocky 9+; agent and CLI shipped as RPMs (server also RPM/container).
- Filesystems: NFS only for MVP. VAST/Weka drivers and the in-house vault-fs FUSE package remain Ansible-deployed; design keeps fstype extensible.
- LDAP/AD: LDAPS required; no nested groups (direct group membership -> role mapping).
- Retention: server audit data kept indefinitely (plan for partitioning/archival); client logs rely on logrotate (~14 days) via /var/log/messages.

## Still open
- Expected client count (design assumes "thousands"; need a target for sizing).
- Exact directory group names -> role mapping (can be configurable, not blocking).
