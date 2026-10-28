-- Everything the client encrypts arrives here as opaque base64. The server
-- never sees master passwords, vault keys, hostnames or SSH credentials.

CREATE TABLE IF NOT EXISTS users (
  id                   TEXT PRIMARY KEY,
  email                TEXT NOT NULL UNIQUE COLLATE NOCASE,
  kdf                  TEXT NOT NULL,   -- client KDF params (JSON)
  salt                 TEXT NOT NULL,   -- client KDF salt (base64)
  auth_hash            BLOB NOT NULL,   -- sha256(auth_salt || client auth key)
  auth_salt            BLOB NOT NULL,
  wrapped_key          TEXT NOT NULL,   -- vault key, AES-GCM wrapped by the password-derived key
  recovery_wrapped_key TEXT NOT NULL,   -- vault key, wrapped by the recovery-code-derived key
  recovery_hash        BLOB NOT NULL,
  recovery_salt        BLOB NOT NULL,
  totp_secret          BLOB,            -- encrypted with the server key; NULL = 2FA off
  totp_pending         BLOB,
  totp_last_step       INTEGER NOT NULL DEFAULT 0,
  vault_revision       INTEGER NOT NULL DEFAULT 0,
  created_at           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash BLOB PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ip         TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS vault_items (
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  id         TEXT NOT NULL,
  data       TEXT NOT NULL,
  revision   INTEGER NOT NULL,
  deleted    INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, id)
);
CREATE INDEX IF NOT EXISTS vault_items_rev ON vault_items(user_id, revision);

CREATE TABLE IF NOT EXISTS audit_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  ip         TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_user ON audit_events(user_id, id DESC);
