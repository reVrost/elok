package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/revrost/elok/pkg/plugins/protocol"
	"github.com/revrost/elok/pkg/tenantctx"
	"modernc.org/quickjs"
)

const (
	pluginID      = "invoker-poc"
	pluginVersion = "0.1.0"

	defaultScriptPath = "plugins/invoker-poc/cmd/invokerpoc/runtime/invoker_poc.js"
	scriptPathEnv     = "ELOK_INVOKER_POC_SCRIPT"

	defaultStatePath = "~/.elok/invoker-poc.json"
	statePathEnv     = "ELOK_INVOKER_POC_STATE"

	defaultInvokerBaseURL = "https://counterspell.io"
	defaultRedirectURI    = "https://counterspell.io/api/v1/auth/callback"
	defaultProvider       = "google"
	defaultLocalURL       = "http://127.0.0.1:7777"
	defaultZone           = "counterspell.app"

	envInvokerBaseURL  = "ELOK_INVOKER_BASE_URL"
	envRedirectURI     = "ELOK_INVOKER_REDIRECT_URI"
	envProvider        = "ELOK_INVOKER_PROVIDER"
	envLocalURL        = "ELOK_INVOKER_LOCAL_URL"
	envZone            = "ELOK_INVOKER_ZONE"
	envCloudflaredPath = "ELOK_INVOKER_CLOUDFLARED_PATH"

	scriptEvalTimeout   = 250 * time.Millisecond
	scriptMemoryLimitMB = 32
	invokerHTTPTimeout  = 30 * time.Second
	authStateTTL        = 10 * time.Minute
)

//go:embed runtime/invoker_poc.js
var embeddedScript string

func main() {
	st := newSessionState()
	rt := newScriptRuntime(resolveScriptPath())
	defer func() {
		if err := rt.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: failed to close script runtime: %v\n", pluginID, err)
		}
	}()

	runtimeState, err := loadRuntimeState(resolveStatePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: failed to load runtime state: %v\n", pluginID, err)
		runtimeState = newRuntimeState(resolveStatePath(), persistedState{})
	}
	defer runtimeState.stopTunnel()

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
		handleCall(st, rt, runtimeState, env)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: stdin scanner error: %v\n", pluginID, err)
	}
}

func handleCall(st *sessionState, rt *scriptRuntime, rs *runtimeState, env protocol.Envelope) {
	switch env.Method {
	case "register":
		sendResult(env.ID, protocol.RegisterResult{
			ID:      pluginID,
			Version: pluginVersion,
			Capabilities: protocol.Capabilities{
				Commands: true,
				Hooks:    false,
				Tools:    false,
			},
		})
	case "command.handle":
		var in protocol.CommandHandleParams
		if err := json.Unmarshal(env.Params, &in); err != nil {
			sendError(env.ID, "bad_params", err.Error())
			return
		}
		out, err := handleScriptCall[commandScriptResult](st, rt, env.Method, scopedSessionID(in.TenantID, in.SessionID), in)
		if err != nil {
			sendError(env.ID, "runtime_error", err.Error())
			return
		}
		if out.Handled && out.Action != nil {
			resp, actionErr := rs.executeAction(context.Background(), *out.Action)
			if actionErr != nil {
				out.Response = "invoker-poc error: " + actionErr.Error()
			} else if strings.TrimSpace(resp) != "" {
				out.Response = resp
			}
		}
		sendResult(env.ID, protocol.CommandHandleResult{
			Handled:  out.Handled,
			Response: out.Response,
		})
	default:
		sendError(env.ID, "method_not_found", "unsupported method: "+env.Method)
	}
}

type commandScriptResult struct {
	Handled  bool           `json:"handled"`
	Response string         `json:"response"`
	Action   *commandAction `json:"action,omitempty"`
}

type commandAction struct {
	Type           string `json:"type"`
	InvokerBaseURL string `json:"invoker_base_url,omitempty"`
	Provider       string `json:"provider,omitempty"`
	RedirectURI    string `json:"redirect_uri,omitempty"`
	LocalURL       string `json:"local_url,omitempty"`
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
	return &sessionState{session: map[string]json.RawMessage{}}
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
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return json.RawMessage(cp)
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

func normalizeState(raw json.RawMessage) (json.RawMessage, bool, error) {
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
		return nil, false, fmt.Errorf("state must be object or null: %w", err)
	}
	out, err := json.Marshal(value)
	if err != nil {
		return nil, false, fmt.Errorf("encode state: %w", err)
	}
	return out, false, nil
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
	return &scriptRuntime{scriptPath: scriptPath}
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
		return empty, fmt.Errorf("dispatch %s: missing result", method)
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
		return r.swapVM(embeddedScript, true)
	}

	info, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat script %s: %w", path, err)
		}
		if r.vm == nil || !r.usingEmbedded {
			if err := r.swapVM(embeddedScript, true); err != nil {
				if r.vm != nil {
					fmt.Fprintf(os.Stderr, "%s: failed to load embedded fallback script: %v\n", pluginID, err)
					return nil
				}
				return err
			}
			fmt.Fprintf(os.Stderr, "%s: %s not found, using embedded script fallback\n", pluginID, path)
		}
		return nil
	}

	fp := fileFingerprint{modTime: info.ModTime().UTC(), size: info.Size()}
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
			fmt.Fprintf(os.Stderr, "%s: failed to reload script %s, keeping previous runtime: %v\n", pluginID, path, err)
			return nil
		}
		return fmt.Errorf("load script %s: %w", path, err)
	}

	wasLoaded := !r.fingerprint.modTime.IsZero() || r.fingerprint.size > 0
	r.fingerprint = fp
	if wasLoaded {
		fmt.Fprintf(os.Stderr, "%s: reloaded script %s\n", pluginID, path)
	} else {
		fmt.Fprintf(os.Stderr, "%s: loaded script %s\n", pluginID, path)
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

type persistedState struct {
	InvokerBaseURL string `json:"invoker_base_url,omitempty"`
	Provider       string `json:"provider,omitempty"`
	RedirectURI    string `json:"redirect_uri,omitempty"`
	LocalURL       string `json:"local_url,omitempty"`
	Zone           string `json:"zone,omitempty"`

	MachineID      string `json:"machine_id,omitempty"`
	MachineJWT     string `json:"machine_jwt,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	UserEmail      string `json:"user_email,omitempty"`
	Subdomain      string `json:"subdomain,omitempty"`
	TunnelToken    string `json:"tunnel_token,omitempty"`
	TunnelProvider string `json:"tunnel_provider,omitempty"`

	PendingState        string `json:"pending_state,omitempty"`
	PendingCodeVerifier string `json:"pending_code_verifier,omitempty"`
	PendingCreatedAtMS  int64  `json:"pending_created_at_ms,omitempty"`
}

type runtimeState struct {
	mu        sync.Mutex
	path      string
	persisted persistedState
	client    *http.Client

	tunnelMu sync.Mutex
	tunnel   *runningTunnel
}

type runningTunnel struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	local   string
	started time.Time
}

func newRuntimeState(path string, st persistedState) *runtimeState {
	return &runtimeState{
		path:      path,
		persisted: st,
		client:    &http.Client{Timeout: invokerHTTPTimeout},
	}
}

func loadRuntimeState(path string) (*runtimeState, error) {
	path = expandPath(path)
	st := persistedState{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newRuntimeState(path, st), nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("decode state file: %w", err)
	}
	return newRuntimeState(path, st), nil
}

func (r *runtimeState) saveLocked() error {
	if strings.TrimSpace(r.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	data, err := json.MarshalIndent(r.persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmpPath := r.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}

func (r *runtimeState) snapshot() persistedState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persisted
}

func (r *runtimeState) updatePersisted(fn func(*persistedState) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := fn(&r.persisted); err != nil {
		return err
	}
	return r.saveLocked()
}

func (r *runtimeState) executeAction(ctx context.Context, a commandAction) (string, error) {
	switch a.Type {
	case "status":
		return r.statusText(), nil
	case "auth_start":
		cfg := r.resolveConfig(a)
		return r.authStart(ctx, cfg)
	case "auth_complete":
		cfg := r.resolveConfig(a)
		msg, _, err := r.authComplete(ctx, cfg)
		return msg, err
	case "register":
		cfg := r.resolveConfig(a)
		return r.registerMachine(ctx, cfg)
	case "tunnel_on":
		cfg := r.resolveConfig(a)
		return r.startTunnel(cfg)
	case "tunnel_off":
		return r.stopTunnel(), nil
	case "pair":
		cfg := r.resolveConfig(a)
		return r.pair(ctx, cfg)
	case "reset":
		r.stopTunnel()
		if err := r.updatePersisted(func(st *persistedState) error {
			st.MachineJWT = ""
			st.UserID = ""
			st.UserEmail = ""
			st.Subdomain = ""
			st.TunnelToken = ""
			st.TunnelProvider = ""
			st.PendingState = ""
			st.PendingCodeVerifier = ""
			st.PendingCreatedAtMS = 0
			return nil
		}); err != nil {
			return "", err
		}
		return "invoker-poc: cleared auth + tunnel metadata", nil
	default:
		return "", fmt.Errorf("unknown action: %s", a.Type)
	}
}

type resolvedConfig struct {
	InvokerBaseURL string
	Provider       string
	RedirectURI    string
	LocalURL       string
	Zone           string
	Cloudflared    string
}

func (r *runtimeState) resolveConfig(a commandAction) resolvedConfig {
	st := r.snapshot()
	cfg := resolvedConfig{
		InvokerBaseURL: firstNonEmpty(strings.TrimSpace(a.InvokerBaseURL), strings.TrimSpace(st.InvokerBaseURL), strings.TrimSpace(os.Getenv(envInvokerBaseURL)), defaultInvokerBaseURL),
		Provider:       firstNonEmpty(strings.TrimSpace(a.Provider), strings.TrimSpace(st.Provider), strings.TrimSpace(os.Getenv(envProvider)), defaultProvider),
		RedirectURI:    firstNonEmpty(strings.TrimSpace(a.RedirectURI), strings.TrimSpace(st.RedirectURI), strings.TrimSpace(os.Getenv(envRedirectURI)), defaultRedirectURI),
		LocalURL:       firstNonEmpty(strings.TrimSpace(a.LocalURL), strings.TrimSpace(st.LocalURL), strings.TrimSpace(os.Getenv(envLocalURL)), defaultLocalURL),
		Zone:           firstNonEmpty(strings.TrimSpace(st.Zone), strings.TrimSpace(os.Getenv(envZone)), defaultZone),
		Cloudflared:    strings.TrimSpace(os.Getenv(envCloudflaredPath)),
	}
	_ = r.updatePersisted(func(p *persistedState) error {
		p.InvokerBaseURL = cfg.InvokerBaseURL
		p.Provider = cfg.Provider
		p.RedirectURI = cfg.RedirectURI
		p.LocalURL = cfg.LocalURL
		p.Zone = cfg.Zone
		return nil
	})
	return cfg
}

func (r *runtimeState) authStart(ctx context.Context, cfg resolvedConfig) (string, error) {
	stateVal, err := generateStateToken()
	if err != nil {
		return "", err
	}
	verifier, err := generateCodeVerifier()
	if err != nil {
		return "", err
	}
	challenge := generateCodeChallenge(verifier)

	req := map[string]string{
		"redirect_uri":   cfg.RedirectURI,
		"code_challenge": challenge,
		"state":          stateVal,
		"provider":       cfg.Provider,
	}
	var resp struct {
		AuthURL string `json:"auth_url"`
	}
	callCtx, cancel := context.WithTimeout(ctx, invokerHTTPTimeout)
	defer cancel()
	if err := r.doInvokerJSON(callCtx, cfg.InvokerBaseURL, http.MethodPost, "/api/v1/auth/url", req, &resp, nil); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.AuthURL) == "" {
		return "", fmt.Errorf("invoker returned empty auth_url")
	}
	if err := r.updatePersisted(func(st *persistedState) error {
		st.PendingState = stateVal
		st.PendingCodeVerifier = verifier
		st.PendingCreatedAtMS = time.Now().UnixMilli()
		return nil
	}); err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"invoker-poc: open this URL and complete login:\n%s\n\nThen run `/cstunnel` again.",
		resp.AuthURL,
	), nil
}

func (r *runtimeState) authComplete(ctx context.Context, cfg resolvedConfig) (string, bool, error) {
	st := r.snapshot()
	if strings.TrimSpace(st.PendingState) == "" {
		return "invoker-poc: no pending auth state; run `/cstunnel` to start pairing", false, nil
	}
	if st.PendingCreatedAtMS > 0 {
		age := time.Since(time.UnixMilli(st.PendingCreatedAtMS))
		if age > authStateTTL {
			_ = r.updatePersisted(func(p *persistedState) error {
				p.PendingState = ""
				p.PendingCodeVerifier = ""
				p.PendingCreatedAtMS = 0
				return nil
			})
			return "invoker-poc: pending auth expired; run `/cstunnel` again", false, nil
		}
	}

	var pollResp struct {
		Status string `json:"status"`
		Code   string `json:"code,omitempty"`
	}
	callCtx, cancel := context.WithTimeout(ctx, invokerHTTPTimeout)
	defer cancel()
	if err := r.doInvokerJSON(callCtx, cfg.InvokerBaseURL, http.MethodPost, "/api/v1/auth/poll", map[string]string{
		"state": st.PendingState,
	}, &pollResp, nil); err != nil {
		return "", false, err
	}
	if strings.ToLower(strings.TrimSpace(pollResp.Status)) != "ready" || strings.TrimSpace(pollResp.Code) == "" {
		return "invoker-poc: auth still pending; finish browser login then run `/cstunnel` again", false, nil
	}

	var exchangeResp struct {
		MachineJWT string `json:"machine_jwt"`
		UserID     string `json:"user_id"`
		UserEmail  string `json:"user_email"`
	}
	if err := r.doInvokerJSON(callCtx, cfg.InvokerBaseURL, http.MethodPost, "/api/v1/auth/exchange", map[string]string{
		"code":          pollResp.Code,
		"state":         st.PendingState,
		"code_verifier": st.PendingCodeVerifier,
	}, &exchangeResp, nil); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(exchangeResp.MachineJWT) == "" {
		return "", false, fmt.Errorf("invoker returned empty machine_jwt")
	}
	if err := r.updatePersisted(func(p *persistedState) error {
		p.MachineJWT = exchangeResp.MachineJWT
		p.UserID = exchangeResp.UserID
		p.UserEmail = exchangeResp.UserEmail
		p.PendingState = ""
		p.PendingCodeVerifier = ""
		p.PendingCreatedAtMS = 0
		return nil
	}); err != nil {
		return "", false, err
	}

	return fmt.Sprintf("invoker-poc: login complete (%s). continuing pairing...", firstNonEmpty(exchangeResp.UserEmail, exchangeResp.UserID, "user")), true, nil
}

func (r *runtimeState) registerMachine(ctx context.Context, cfg resolvedConfig) (string, error) {
	st := r.snapshot()
	if strings.TrimSpace(st.MachineJWT) == "" {
		return "", fmt.Errorf("not authenticated; run `/cstunnel`")
	}

	machineID := strings.TrimSpace(st.MachineID)
	if machineID == "" {
		machineID = deriveMachineID()
		if err := r.updatePersisted(func(p *persistedState) error {
			p.MachineID = machineID
			return nil
		}); err != nil {
			return "", err
		}
	}
	hostname, _ := os.Hostname()

	req := map[string]string{
		"machine_id": machineID,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"hostname":   hostname,
		"version":    pluginVersion,
	}
	headers := map[string]string{
		"Authorization": "Bearer " + st.MachineJWT,
	}
	var resp struct {
		UserID         string `json:"user_id"`
		Subdomain      string `json:"subdomain"`
		TunnelToken    string `json:"tunnel_token"`
		TunnelProvider string `json:"tunnel_provider"`
	}
	callCtx, cancel := context.WithTimeout(ctx, invokerHTTPTimeout)
	defer cancel()
	if err := r.doInvokerJSON(callCtx, cfg.InvokerBaseURL, http.MethodPost, "/api/v1/machines/register", req, &resp, headers); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Subdomain) == "" || strings.TrimSpace(resp.TunnelToken) == "" {
		return "", fmt.Errorf("invoker registration missing subdomain or tunnel_token")
	}
	if err := r.updatePersisted(func(p *persistedState) error {
		p.UserID = firstNonEmpty(resp.UserID, p.UserID)
		p.Subdomain = resp.Subdomain
		p.TunnelToken = resp.TunnelToken
		p.TunnelProvider = resp.TunnelProvider
		return nil
	}); err != nil {
		return "", err
	}

	publicURL := formatPublicURL(resp.Subdomain, cfg.Zone)
	return fmt.Sprintf("invoker-poc: machine registered.\nsubdomain: %s\ntunnel_provider: %s\npublic_url: %s", resp.Subdomain, firstNonEmpty(resp.TunnelProvider, "unknown"), publicURL), nil
}

func (r *runtimeState) startTunnel(cfg resolvedConfig) (string, error) {
	st := r.snapshot()
	if strings.TrimSpace(st.TunnelToken) == "" {
		return "", fmt.Errorf("missing tunnel token; run `/cstunnel`")
	}

	r.tunnelMu.Lock()
	if r.tunnel != nil && r.tunnel.cmd != nil && r.tunnel.cmd.Process != nil && r.tunnel.cmd.ProcessState == nil {
		r.tunnelMu.Unlock()
		return "invoker-poc: tunnel already running", nil
	}
	r.tunnelMu.Unlock()

	binPath := strings.TrimSpace(cfg.Cloudflared)
	if binPath == "" {
		var err error
		binPath, err = exec.LookPath("cloudflared")
		if err != nil {
			return "", fmt.Errorf("cloudflared not found in PATH (or set %s)", envCloudflaredPath)
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, binPath, "tunnel", "--protocol", "http2", "--no-autoupdate", "run", "--token", st.TunnelToken, "--url", cfg.LocalURL)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", fmt.Errorf("cloudflared stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return "", fmt.Errorf("cloudflared stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("start cloudflared: %w", err)
	}

	t := &runningTunnel{
		cmd:     cmd,
		cancel:  cancel,
		local:   cfg.LocalURL,
		started: time.Now(),
	}

	r.tunnelMu.Lock()
	r.tunnel = t
	r.tunnelMu.Unlock()

	logPipe("stdout", stdout)
	logPipe("stderr", stderr)
	go r.waitTunnel(t)

	publicURL := formatPublicURL(st.Subdomain, cfg.Zone)
	return fmt.Sprintf("invoker-poc: tunnel started.\npublic_url: %s\nlocal_url: %s", publicURL, cfg.LocalURL), nil
}

func (r *runtimeState) waitTunnel(t *runningTunnel) {
	err := t.cmd.Wait()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: cloudflared exited with error: %v\n", pluginID, err)
	} else {
		fmt.Fprintf(os.Stderr, "%s: cloudflared exited\n", pluginID)
	}
	r.tunnelMu.Lock()
	if r.tunnel == t {
		r.tunnel = nil
	}
	r.tunnelMu.Unlock()
}

func (r *runtimeState) stopTunnel() string {
	r.tunnelMu.Lock()
	t := r.tunnel
	r.tunnel = nil
	r.tunnelMu.Unlock()

	if t == nil {
		return "invoker-poc: tunnel is not running"
	}
	if t.cancel != nil {
		t.cancel()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return "invoker-poc: tunnel stopped"
}

func (r *runtimeState) tunnelRunning() bool {
	r.tunnelMu.Lock()
	defer r.tunnelMu.Unlock()
	return r.tunnel != nil && r.tunnel.cmd != nil && r.tunnel.cmd.Process != nil && r.tunnel.cmd.ProcessState == nil
}

func (r *runtimeState) pair(ctx context.Context, cfg resolvedConfig) (string, error) {
	messages := make([]string, 0, 4)
	st := r.snapshot()

	if strings.TrimSpace(st.MachineJWT) == "" {
		if strings.TrimSpace(st.PendingState) == "" {
			msg, err := r.authStart(ctx, cfg)
			if err != nil {
				return "", err
			}
			return msg, nil
		}
		msg, done, err := r.authComplete(ctx, cfg)
		if err != nil {
			return "", err
		}
		messages = append(messages, msg)
		if !done {
			return strings.Join(messages, "\n\n"), nil
		}
	}

	st = r.snapshot()
	if strings.TrimSpace(st.Subdomain) == "" || strings.TrimSpace(st.TunnelToken) == "" {
		msg, err := r.registerMachine(ctx, cfg)
		if err != nil {
			return "", err
		}
		messages = append(messages, msg)
	}

	if !r.tunnelRunning() {
		msg, err := r.startTunnel(cfg)
		if err != nil {
			return "", err
		}
		messages = append(messages, msg)
	}

	if len(messages) == 0 {
		messages = append(messages, r.statusText())
	}
	return strings.Join(messages, "\n\n"), nil
}

func (r *runtimeState) statusText() string {
	st := r.snapshot()
	cfg := r.resolveConfig(commandAction{})
	lines := []string{
		"cstunnel status:",
		"- invoker_base_url: " + cfg.InvokerBaseURL,
		"- provider: " + cfg.Provider,
		"- redirect_uri: " + cfg.RedirectURI,
		"- local_url: " + cfg.LocalURL,
		"- machine_id: " + valueOr(st.MachineID, "(none)"),
		"- machine_jwt: " + yesNo(strings.TrimSpace(st.MachineJWT) != ""),
		"- pending_auth: " + yesNo(strings.TrimSpace(st.PendingState) != ""),
		"- subdomain: " + valueOr(st.Subdomain, "(none)"),
		"- tunnel_provider: " + valueOr(st.TunnelProvider, "(none)"),
		"- tunnel_token: " + maskToken(st.TunnelToken),
		"- tunnel_running: " + yesNo(r.tunnelRunning()),
	}
	if strings.TrimSpace(st.Subdomain) != "" {
		lines = append(lines, "- public_url: "+formatPublicURL(st.Subdomain, cfg.Zone))
	}
	return strings.Join(lines, "\n")
}

func (r *runtimeState) doInvokerJSON(ctx context.Context, baseURL, method, path string, reqBody any, respBody any, headers map[string]string) error {
	var body io.Reader
	if reqBody != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = buf
	}

	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("invoker %s %s failed: %s", method, path, msg)
	}
	if respBody == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
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

func resolveStatePath() string {
	path := strings.TrimSpace(os.Getenv(statePathEnv))
	if path == "" {
		path = defaultStatePath
	}
	return filepath.Clean(expandPath(path))
}

func expandPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return trimmed
		}
		return home
	}
	if strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return trimmed
		}
		return filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
	}
	return trimmed
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
		fmt.Fprintf(os.Stderr, "%s: failed to encode envelope: %v\n", pluginID, err)
		return
	}
	fmt.Println(string(data))
}

func logPipe(label string, pipe io.Reader) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 16*1024), 512*1024)
	go func() {
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			fmt.Fprintf(os.Stderr, "%s cloudflared %s: %s\n", pluginID, label, line)
		}
	}()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func maskToken(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return "(none)"
	}
	if len(t) <= 10 {
		return strings.Repeat("*", len(t))
	}
	return t[:6] + "..." + t[len(t)-4:]
}

func formatPublicURL(subdomain, zone string) string {
	s := strings.TrimSpace(subdomain)
	z := strings.TrimSpace(zone)
	if s == "" {
		return "(unknown)"
	}
	if strings.Contains(s, ".") {
		return "https://" + s
	}
	if z == "" {
		z = defaultZone
	}
	return "https://" + s + "." + z
}

func generateStateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate code verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func generateCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func deriveMachineID() string {
	host, _ := os.Hostname()
	user := firstNonEmpty(os.Getenv("USER"), os.Getenv("USERNAME"))
	src := strings.Join([]string{host, user, runtime.GOOS, runtime.GOARCH}, "|")
	sum := sha256.Sum256([]byte(src))
	return "elok-" + hex.EncodeToString(sum[:8])
}
