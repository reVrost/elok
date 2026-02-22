package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/revrost/elok/pkg/plugins/protocol"
)

type state struct {
	mu      sync.RWMutex
	enabled map[string]bool
}

func main() {
	st := &state{enabled: map[string]bool{}}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			sendError("", "invalid_json", err.Error())
			continue
		}
		if env.Type != protocol.TypeCall {
			continue
		}
		handleCall(st, env)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin scanner error: %v\n", err)
	}
}

func handleCall(st *state, env protocol.Envelope) {
	switch env.Method {
	case "register":
		sendResult(env.ID, protocol.RegisterResult{
			ID:      "plan-mode",
			Version: "0.1.0",
			Capabilities: protocol.Capabilities{
				Commands: true,
				Hooks:    true,
				Tools:    false,
			},
		})
	case "command.handle":
		var in protocol.CommandHandleParams
		if err := json.Unmarshal(env.Params, &in); err != nil {
			sendError(env.ID, "bad_params", err.Error())
			return
		}
		out := handleCommand(st, in)
		sendResult(env.ID, out)
	case "hook.before_turn":
		var in protocol.HookBeforeTurnParams
		if err := json.Unmarshal(env.Params, &in); err != nil {
			sendError(env.ID, "bad_params", err.Error())
			return
		}
		out := handleBeforeTurn(st, in)
		sendResult(env.ID, out)
	case "hook.after_turn":
		sendResult(env.ID, map[string]any{"ok": true})
	default:
		sendError(env.ID, "method_not_found", "unsupported method: "+env.Method)
	}
}

func handleCommand(st *state, in protocol.CommandHandleParams) protocol.CommandHandleResult {
	text := strings.TrimSpace(in.Text)
	if !strings.HasPrefix(text, "/plan") {
		return protocol.CommandHandleResult{Handled: false}
	}
	parts := strings.Fields(text)
	if len(parts) == 1 {
		return protocol.CommandHandleResult{
			Handled:  true,
			Response: "usage: /plan on | /plan off | /plan status",
		}
	}
	mode := strings.ToLower(parts[1])
	switch mode {
	case "on":
		st.mu.Lock()
		st.enabled[in.SessionID] = true
		st.mu.Unlock()
		return protocol.CommandHandleResult{Handled: true, Response: "plan mode: ON"}
	case "off":
		st.mu.Lock()
		delete(st.enabled, in.SessionID)
		st.mu.Unlock()
		return protocol.CommandHandleResult{Handled: true, Response: "plan mode: OFF"}
	case "status":
		st.mu.RLock()
		enabled := st.enabled[in.SessionID]
		st.mu.RUnlock()
		if enabled {
			return protocol.CommandHandleResult{Handled: true, Response: "plan mode is ON"}
		}
		return protocol.CommandHandleResult{Handled: true, Response: "plan mode is OFF"}
	default:
		return protocol.CommandHandleResult{Handled: true, Response: "usage: /plan on | /plan off | /plan status"}
	}
}

func handleBeforeTurn(st *state, in protocol.HookBeforeTurnParams) protocol.HookBeforeTurnResult {
	st.mu.RLock()
	enabled := st.enabled[in.SessionID]
	st.mu.RUnlock()
	if !enabled {
		return protocol.HookBeforeTurnResult{UserText: in.UserText}
	}
	augmented := strings.TrimSpace(`
You are in plan mode.
Before writing the final answer, produce a concise execution plan.
Then continue with the answer.

User request:
` + in.UserText)
	return protocol.HookBeforeTurnResult{
		UserText:           augmented,
		SystemPromptAppend: "Plan mode is enabled for this session.",
	}
}

func sendResult(id string, result any) {
	payload, err := json.Marshal(result)
	if err != nil {
		sendError(id, "marshal_error", err.Error())
		return
	}
	writeEnvelope(protocol.Envelope{
		Type:   protocol.TypeResult,
		ID:     id,
		Result: payload,
	})
}

func sendError(id, code, message string) {
	writeEnvelope(protocol.Envelope{
		Type: protocol.TypeError,
		ID:   id,
		Error: &protocol.RPCError{
			Code:    code,
			Message: message,
		},
	})
}

func writeEnvelope(env protocol.Envelope) {
	data, err := json.Marshal(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode envelope: %v\n", err)
		return
	}
	fmt.Println(string(data))
}
