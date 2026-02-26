CREATE TABLE IF NOT EXISTS runtime_llm_config (
  tenant_id TEXT PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  openrouter_api_key TEXT NOT NULL DEFAULT '',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
