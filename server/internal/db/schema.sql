CREATE TABLE IF NOT EXISTS templates (
  id bigserial PRIMARY KEY, name text UNIQUE NOT NULL, fstype text NOT NULL DEFAULT 'nfs',
  source text NOT NULL, mountpoint text NOT NULL, options text NOT NULL DEFAULT '',
  version int NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS groups (
  id bigserial PRIMARY KEY, name text UNIQUE NOT NULL, priority int NOT NULL DEFAULT 100,
  host_regex text NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS group_templates (
  group_id bigint REFERENCES groups ON DELETE CASCADE, template_id bigint REFERENCES templates ON DELETE CASCADE,
  PRIMARY KEY (group_id, template_id));
CREATE TABLE IF NOT EXISTS hosts (
  id bigserial PRIMARY KEY, hostname text UNIQUE NOT NULL, cert_serial text NOT NULL DEFAULT '',
  agent_version text NOT NULL DEFAULT '', last_seen timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
  desired_hash text NOT NULL DEFAULT '', applied_hash text NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS group_members (
  group_id bigint REFERENCES groups ON DELETE CASCADE, host_id bigint REFERENCES hosts ON DELETE CASCADE,
  PRIMARY KEY (group_id, host_id));
CREATE TABLE IF NOT EXISTS host_mounts (
  host_id bigint REFERENCES hosts ON DELETE CASCADE, mountpoint text NOT NULL,
  state text NOT NULL, error text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (host_id, mountpoint));
CREATE TABLE IF NOT EXISTS audit (
  id bigserial PRIMARY KEY, ts timestamptz NOT NULL DEFAULT now(), actor text NOT NULL,
  host text NOT NULL DEFAULT '', action text NOT NULL, detail text NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS audit_ts ON audit (ts);
CREATE TABLE IF NOT EXISTS users (
  id bigserial PRIMARY KEY, username text UNIQUE NOT NULL, pass_hash text NOT NULL,
  role text NOT NULL DEFAULT 'admin', source text NOT NULL DEFAULT 'local');
CREATE TABLE IF NOT EXISTS sessions (
  token_hash text PRIMARY KEY, username text NOT NULL, role text NOT NULL, expires timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS enroll_tokens (
  token_hash text PRIMARY KEY, note text NOT NULL DEFAULT '', expires timestamptz NOT NULL, uses_left int NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS kv (k text PRIMARY KEY, v bytea NOT NULL);

-- Enrolment token lifecycle (listing, remaining uses, revocation). Additive and idempotent.
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS id bigserial;
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT '';
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS uses_total int NOT NULL DEFAULT 0;
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
UPDATE enroll_tokens SET uses_total = GREATEST(uses_left, 1) WHERE uses_total = 0;
CREATE UNIQUE INDEX IF NOT EXISTS enroll_tokens_id ON enroll_tokens (id);
CREATE INDEX IF NOT EXISTS hosts_hostname_lower ON hosts (lower(hostname));
