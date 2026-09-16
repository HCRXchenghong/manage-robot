-- 0006_identity.sql: durable organization and operator identities. Runtime maps
-- remain caches; PostgreSQL is the authority across fleet-hub restarts.
CREATE TABLE IF NOT EXISTS org_groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  max_admins INT NOT NULL DEFAULT 5,
  max_users INT NOT NULL DEFAULT 10,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_users (
  username TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('super', 'group_admin', 'user')),
  group_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
  phone TEXT UNIQUE,
  pass_hash TEXT NOT NULL,
  salt TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  pass_set_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
