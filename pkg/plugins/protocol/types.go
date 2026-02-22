package protocol

import "encoding/json"

const (
	TypeCall   = "call"
	TypeResult = "result"
	TypeError  = "error"
	TypeEvent  = "event"
)

type Envelope struct {
	Type   string          `json:"type"`
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
	Event  string          `json:"event,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RegisterResult struct {
	ID           string       `json:"id"`
	Version      string       `json:"version"`
	Capabilities Capabilities `json:"capabilities"`
}

type Capabilities struct {
	Commands bool `json:"commands"`
	Hooks    bool `json:"hooks"`
	Tools    bool `json:"tools"`
}

type CommandHandleParams struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type CommandHandleResult struct {
	Handled  bool   `json:"handled"`
	Response string `json:"response"`
}

type HookBeforeTurnParams struct {
	SessionID string `json:"session_id"`
	UserText  string `json:"user_text"`
}

type HookBeforeTurnResult struct {
	UserText           string `json:"user_text"`
	SystemPromptAppend string `json:"system_prompt_append"`
}

type HookAfterTurnParams struct {
	SessionID     string `json:"session_id"`
	UserText      string `json:"user_text"`
	AssistantText string `json:"assistant_text"`
}
