-- name: GetRuntimeLLMConfig :one
SELECT tenant_id, provider, model, openrouter_api_key, updated_at
FROM runtime_llm_config
WHERE tenant_id = ?;

-- name: UpsertRuntimeLLMConfig :exec
INSERT INTO runtime_llm_config (tenant_id, provider, model, openrouter_api_key, updated_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(tenant_id) DO UPDATE SET
  provider = excluded.provider,
  model = excluded.model,
  openrouter_api_key = excluded.openrouter_api_key,
  updated_at = CURRENT_TIMESTAMP;
