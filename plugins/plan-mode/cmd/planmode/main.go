package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/revrost/elok/pkg/plugins/protocol"
	"github.com/revrost/elok/pkg/tenantctx"
	"modernc.org/quickjs"
)

const (
	defaultScriptPath   = "plugins/plan-mode/cmd/planmode/runtime/plan_mode.js"
	scriptPathEnv       = "ELOK_PLANMODE_SCRIPT"
	scriptEvalTimeout   = 250 * time.Millisecond
	scriptMemoryLimitMB = 32
)

//go:embed runtime/plan_mode.js
var embeddedPlanModeScript string

func main() {
	st := newSessionState()
	rt := newScriptRuntime(resolveScriptPath())
	defer func() {
		if err := rt.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close script runtime: %v\n", err)
		}
	}()

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
		handleCall(st, rt, env)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin scanner error: %v\n", err)
	}
}

func handleCall(st *sessionState, rt *scriptRuntime, env protocol.Envelope) {
	switch env.Method {
	case "register":
		sendResult(env.ID, protocol.RegisterResult{
			ID:      "plan-mode",
			Version: "0.2.0",
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
		out, err := handleScriptCall[protocol.CommandHandleResult](st, rt, env.Method, scopedSessionID(in.TenantID, in.SessionID), in)
		if err != nil {
			sendError(env.ID, "runtime_error", err.Error())
			return
		}
		sendResult(env.ID, out)
	case "hook.before_turn":
		var in protocol.HookBeforeTurnParams
		if err := json.Unmarshal(env.Params, &in); err != nil {
			sendError(env.ID, "bad_params", err.Error())
			return
		}
		out, err := handleScriptCall[protocol.HookBeforeTurnResult](st, rt, env.Method, scopedSessionID(in.TenantID, in.SessionID), in)
		if err != nil {
			sendError(env.ID, "runtime_error", err.Error())
			return
		}
		sendResult(env.ID, out)
	case "hook.after_turn":
		var in protocol.HookAfterTurnParams
		if err := json.Unmarshal(env.Params, &in); err != nil {
			sendError(env.ID, "bad_params", err.Error())
			return
		}
		out, err := handleScriptCall[map[string]any](st, rt, env.Method, scopedSessionID(in.TenantID, in.SessionID), in)
		if err != nil {
			sendError(env.ID, "runtime_error", err.Error())
			return
		}
		sendResult(env.ID, out)
	default:
		sendError(env.ID, "method_not_found", "unsupported method: "+env.Method)
	}
}

func handleScriptCall[T any](st *sessionState, rt *scriptRuntime, method, sessionID string, in any) (T, error) {
	var zero T

	reply, err := rt.Dispatch(method, in, st.Get(sessionID))
	if err != nil {
		return zero, err
	}
	if err := st.Set(sessionID, reply.State); err != nil {
		return zero, err
	}

	var out T
	if err := json.Unmarshal(reply.Result, &out); err != nil {
		return zero, fmt.Errorf("decode %s result: %w", method, err)
	}
	return out, nil
}

func scopedSessionID(tenantID, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return tenantctx.Normalize(tenantID) + ":" + sessionID
}

type sessionState struct {
	mu      sync.RWMutex
	session map[string]json.RawMessage
}

func newSessionState() *sessionState {
	return &sessionState{
		session: map[string]json.RawMessage{},
	}
}

func (s *sessionState) Get(sessionID string) json.RawMessage {
	if strings.TrimSpace(sessionID) == "" {
		return json.RawMessage(`{}`)
	}
	s.mu.RLock()
	raw, ok := s.session[sessionID]
	s.mu.RUnlock()
	if !ok || len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return copyRawMessage(raw)
}

func (s *sessionState) Set(sessionID string, raw json.RawMessage) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	normalized, shouldDelete, err := normalizeState(raw)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if shouldDelete {
		delete(s.session, sessionID)
		return nil
	}
	s.session[sessionID] = normalized
	return nil
}

func normalizeState(raw json.RawMessage) (normalized json.RawMessage, shouldDelete bool, err error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), false, nil
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return json.RawMessage(`{}`), false, nil
	}
	if trimmed == "null" {
		return nil, true, nil
	}

	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, fmt.Errorf("state must be an object or null: %w", err)
	}
	normalized, err = json.Marshal(value)
	if err != nil {
		return nil, false, fmt.Errorf("encode normalized state: %w", err)
	}
	return normalized, false, nil
}

func copyRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return json.RawMessage(cp)
}

type scriptRuntime struct {
	mu            sync.Mutex
	scriptPath    string
	fingerprint   fileFingerprint
	usingEmbedded bool
	vm            *quickjs.VM
}

type fileFingerprint struct {
	modTime time.Time
	size    int64
}

type scriptReply struct {
	Result json.RawMessage `json:"result"`
	State  json.RawMessage `json:"state"`
}

func newScriptRuntime(scriptPath string) *scriptRuntime {
	return &scriptRuntime{
		scriptPath: scriptPath,
	}
}

func (r *scriptRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vm == nil {
		return nil
	}
	err := r.vm.Close()
	r.vm = nil
	return err
}

func (r *scriptRuntime) Dispatch(method string, params any, state json.RawMessage) (scriptReply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var empty scriptReply
	if err := r.ensureLoaded(); err != nil {
		return empty, err
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return empty, fmt.Errorf("encode params for %s: %w", method, err)
	}
	if len(state) == 0 {
		state = json.RawMessage(`{}`)
	}

	out, err := r.vm.Call("dispatch", method, string(paramsJSON), string(state))
	if err != nil {
		return empty, fmt.Errorf("dispatch %s: %w", method, err)
	}
	replyRaw, ok := out.(string)
	if !ok {
		return empty, fmt.Errorf("dispatch %s: expected string response, got %T", method, out)
	}

	var reply scriptReply
	if err := json.Unmarshal([]byte(replyRaw), &reply); err != nil {
		return empty, fmt.Errorf("decode dispatch %s payload: %w", method, err)
	}
	if len(reply.Result) == 0 {
		return empty, fmt.Errorf("dispatch %s: missing result field", method)
	}
	if len(reply.State) == 0 {
		reply.State = state
	}
	return reply, nil
}

func (r *scriptRuntime) ensureLoaded() error {
	path := strings.TrimSpace(r.scriptPath)
	if path == "" {
		if r.vm != nil {
			return nil
		}
		return r.swapVM(embeddedPlanModeScript, true)
	}

	info, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat script %s: %w", path, err)
		}
		if r.vm == nil || !r.usingEmbedded {
			if err := r.swapVM(embeddedPlanModeScript, true); err != nil {
				if r.vm != nil {
					fmt.Fprintf(os.Stderr, "plan-mode: failed to load embedded fallback script: %v\n", err)
					return nil
				}
				return err
			}
			fmt.Fprintf(os.Stderr, "plan-mode: %s not found, using embedded script fallback\n", path)
		}
		return nil
	}

	fp := fileFingerprint{
		modTime: info.ModTime().UTC(),
		size:    info.Size(),
	}
	sameFile := r.vm != nil &&
		!r.usingEmbedded &&
		r.fingerprint.size == fp.size &&
		r.fingerprint.modTime.Equal(fp.modTime)
	if sameFile {
		return nil
	}

	script, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read script %s: %w", path, err)
	}
	if err := r.swapVM(string(script), false); err != nil {
		if r.vm != nil {
			fmt.Fprintf(os.Stderr, "plan-mode: failed to reload script %s, keeping previous runtime: %v\n", path, err)
			return nil
		}
		return fmt.Errorf("load script %s: %w", path, err)
	}

	wasLoaded := !r.fingerprint.modTime.IsZero() || r.fingerprint.size > 0
	r.fingerprint = fp
	if wasLoaded {
		fmt.Fprintf(os.Stderr, "plan-mode: reloaded script %s\n", path)
	} else {
		fmt.Fprintf(os.Stderr, "plan-mode: loaded script %s\n", path)
	}
	return nil
}

func (r *scriptRuntime) swapVM(script string, usingEmbedded bool) error {
	vm, err := quickjs.NewVM()
	if err != nil {
		return fmt.Errorf("create vm: %w", err)
	}
	vm.SetEvalTimeout(scriptEvalTimeout)
	vm.SetMemoryLimit(scriptMemoryLimitMB * 1024 * 1024)

	if _, err := vm.Eval(script, quickjs.EvalGlobal); err != nil {
		_ = vm.Close()
		return fmt.Errorf("evaluate script: %w", err)
	}
	hasDispatchAny, err := vm.Eval("typeof dispatch === 'function'", quickjs.EvalGlobal)
	if err != nil {
		_ = vm.Close()
		return fmt.Errorf("check dispatch symbol: %w", err)
	}
	hasDispatch, ok := hasDispatchAny.(bool)
	if !ok || !hasDispatch {
		_ = vm.Close()
		return fmt.Errorf("script must define function dispatch(method, paramsJSON, stateJSON)")
	}

	old := r.vm
	r.vm = vm
	r.usingEmbedded = usingEmbedded
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func resolveScriptPath() string {
	path := strings.TrimSpace(os.Getenv(scriptPathEnv))
	if path == "" {
		path = defaultScriptPath
	}
	return filepath.Clean(path)
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
