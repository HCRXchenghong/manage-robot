-- 0008_auth_sessions.sql: PostgreSQL-backed operator sessions.
-- The browser token is never persisted. Only a one-way SHA-256 token digest
-- is stored, so a database read cannot be used as an active login credential.
CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash TEXT PRIMARY KEY,
  username TEXT NOT NULL REFERENCES auth_users(username) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('super', 'group_admin', 'user')),
  group_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_at TIMESTAMPTZ NOT NULL,
  last_use_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS auth_sessions_active_idx
  ON auth_sessions (last_use_at, created_at)
  WHERE revoked_at IS NULL;
