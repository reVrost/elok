CREATE TABLE sessions_v2 (
  tenant_id TEXT NOT NULL DEFAULT 'default',
  id TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_message_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id, id)
);

CREATE TABLE messages_v2 (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id TEXT NOT NULL DEFAULT 'default',
  session_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(tenant_id, session_id) REFERENCES sessions_v2(tenant_id, id) ON DELETE CASCADE
);

INSERT INTO sessions_v2 (tenant_id, id, created_at, updated_at, last_message_at)
SELECT 'default', id, created_at, updated_at, last_message_at
FROM sessions;

INSERT INTO messages_v2 (id, tenant_id, session_id, role, content, created_at)
SELECT id, 'default', session_id, role, content, created_at
FROM messages;

DROP TABLE messages;
DROP TABLE sessions;

ALTER TABLE sessions_v2 RENAME TO sessions;
ALTER TABLE messages_v2 RENAME TO messages;

CREATE INDEX idx_messages_tenant_session_id ON messages(tenant_id, session_id, id);
CREATE INDEX idx_sessions_tenant_last_message_at ON sessions(tenant_id, last_message_at DESC);
