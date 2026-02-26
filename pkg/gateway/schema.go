package gateway

import "encoding/json"

const (
	EnvelopeTypeCall   = "call"
	EnvelopeTypeResult = "result"
	EnvelopeTypeError  = "error"
	EnvelopeTypeEvent  = "event"
)

type Envelope struct {
	Type   string          `json:"type"`
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
	Event  string          `json:"event,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type EnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SessionSendParams struct {
	SessionID string `json:"session_id"`
	TenantID  string `json:"tenant_id,omitempty"`
	Text      string `json:"text"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

type SessionSendResult struct {
	SessionID      string `json:"session_id"`
	AssistantText  string `json:"assistant_text"`
	HandledCommand bool   `json:"handled_command"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
}

type SessionListParams struct {
	TenantID string `json:"tenant_id,omitempty"`
	Limit    int    `json:"limit"`
}

type SessionMessagesParams struct {
	SessionID string `json:"session_id"`
	TenantID  string `json:"tenant_id,omitempty"`
	Limit     int    `json:"limit"`
}

type SystemChannelsResult struct {
	Channels []ChannelStatus `json:"channels"`
}

type SystemConfigParams struct {
	TenantID string `json:"tenant_id,omitempty"`
}

type SystemConfigSetParams struct {
	TenantID         string  `json:"tenant_id,omitempty"`
	Provider         *string `json:"provider,omitempty"`
	Model            *string `json:"model,omitempty"`
	OpenRouterAPIKey *string `json:"openrouter_api_key,omitempty"`
}

type SystemConfigResult struct {
	Provider               string `json:"provider,omitempty"`
	Model                  string `json:"model,omitempty"`
	HasOpenRouterAPIKey    bool   `json:"has_openrouter_api_key"`
	OpenRouterAPIKeyMasked string `json:"openrouter_api_key_masked,omitempty"`
}

type SystemCommandsResult struct {
	Commands []CommandHint `json:"commands"`
}

type CommandHint struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
}

type ChannelStatus struct {
	ChannelID string `json:"channel_id"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Connected bool   `json:"connected"`
	LastError string `json:"last_error,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
	UpdatedAt string `json:"updated_at"`
}
