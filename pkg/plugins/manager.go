package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/revrost/elok/pkg/config"
	"github.com/revrost/elok/pkg/plugins/protocol"
	"github.com/revrost/elok/pkg/tenantctx"
)

type Manager struct {
	log     *slog.Logger
	plugins []*runtimePlugin
}

type runtimePlugin struct {
	log      *slog.Logger
	spec     config.PluginSpec
	manifest protocol.RegisterResult

	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu      sync.Mutex
	seq     uint64
	pending map[string]chan protocol.Envelope
}

type AfterTurnParams struct {
	TenantID      string
	SessionID     string
	UserText      string
	AssistantText string
}

func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "plugins")
	return &Manager{log: log}
}

func (m *Manager) Start(ctx context.Context, cfg config.PluginConfig) error {
	if !cfg.Enabled {
		m.log.Info("plugins disabled")
		return nil
	}
	for _, entry := range cfg.Entries {
		if len(entry.Command) == 0 {
			m.log.Warn("skipping plugin entry with empty command", "id", entry.ID)
			continue
		}
		plugin, err := startPlugin(ctx, m.log, entry)
		if err != nil {
			return err
		}
		m.plugins = append(m.plugins, plugin)
		m.log.Info("plugin loaded", "id", plugin.manifest.ID, "version", plugin.manifest.Version)
	}
	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	var firstErr error
	for _, plugin := range m.plugins {
		if err := plugin.stop(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.plugins = nil
	return firstErr
}

func (m *Manager) HandleCommand(ctx context.Context, sessionID, text string) (bool, string, error) {
	tenantID := tenantctx.TenantID(ctx)
	for _, plugin := range m.plugins {
		if !plugin.manifest.Capabilities.Commands {
			continue
		}
		var out protocol.CommandHandleResult
		err := plugin.call(ctx, "command.handle", protocol.CommandHandleParams{
			TenantID:  tenantID,
			SessionID: sessionID,
			Text:      text,
		}, &out)
		if err != nil {
			return false, "", fmt.Errorf("plugin %s command.handle: %w", plugin.spec.ID, err)
		}
		if out.Handled {
			return true, out.Response, nil
		}
	}
	return false, "", nil
}

func (m *Manager) BeforeTurn(ctx context.Context, sessionID, userText string) (string, string, error) {
	tenantID := tenantctx.TenantID(ctx)
	currentText := userText
	systemAdditions := make([]string, 0)
	for _, plugin := range m.plugins {
		if !plugin.manifest.Capabilities.Hooks {
			continue
		}
		var out protocol.HookBeforeTurnResult
		err := plugin.call(ctx, "hook.before_turn", protocol.HookBeforeTurnParams{
			TenantID:  tenantID,
			SessionID: sessionID,
			UserText:  currentText,
		}, &out)
		if err != nil {
			return userText, "", fmt.Errorf("plugin %s hook.before_turn: %w", plugin.spec.ID, err)
		}
		if strings.TrimSpace(out.UserText) != "" {
			currentText = out.UserText
		}
		if strings.TrimSpace(out.SystemPromptAppend) != "" {
			systemAdditions = append(systemAdditions, out.SystemPromptAppend)
		}
	}
	return currentText, strings.Join(systemAdditions, "\n\n"), nil
}

func (m *Manager) AfterTurn(ctx context.Context, in AfterTurnParams) {
	for _, plugin := range m.plugins {
		if !plugin.manifest.Capabilities.Hooks {
			continue
		}
		err := plugin.call(ctx, "hook.after_turn", protocol.HookAfterTurnParams{
			TenantID:      tenantctx.Normalize(in.TenantID),
			SessionID:     in.SessionID,
			UserText:      in.UserText,
			AssistantText: in.AssistantText,
		}, nil)
		if err != nil {
			m.log.Warn("plugin after_turn failed", "plugin", plugin.spec.ID, "error", err)
		}
	}
}

func startPlugin(ctx context.Context, log *slog.Logger, spec config.PluginSpec) (*runtimePlugin, error) {
	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %s stdin pipe: %w", spec.ID, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %s stdout pipe: %w", spec.ID, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %s stderr pipe: %w", spec.ID, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin %s: %w", spec.ID, err)
	}

	plugin := &runtimePlugin{
		log:     log.With("plugin", spec.ID),
		spec:    spec,
		cmd:     cmd,
		stdin:   stdin,
		pending: map[string]chan protocol.Envelope{},
	}
	go plugin.readLoop(stdout)
	go plugin.stderrLoop(stderr)

	ctxRegister, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := plugin.call(ctxRegister, "register", map[string]any{}, &plugin.manifest); err != nil {
		_ = plugin.stop(context.Background())
		return nil, fmt.Errorf("register plugin %s: %w", spec.ID, err)
	}
	if strings.TrimSpace(plugin.manifest.ID) == "" {
		_ = plugin.stop(context.Background())
		return nil, fmt.Errorf("register plugin %s: missing id", spec.ID)
	}
	return plugin, nil
}

func (p *runtimePlugin) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			p.log.Warn("failed to decode plugin output", "line", line, "error", err)
			continue
		}
		switch env.Type {
		case protocol.TypeResult, protocol.TypeError:
			p.mu.Lock()
			ch, ok := p.pending[env.ID]
			if ok {
				delete(p.pending, env.ID)
			}
			p.mu.Unlock()
			if ok {
				ch <- env
			}
		case protocol.TypeEvent:
			p.log.Debug("plugin event", "event", env.Event)
		default:
			p.log.Debug("plugin message", "type", env.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		p.log.Warn("plugin stdout scanner ended with error", "error", err)
	}
	p.failPending("plugin stdout closed")
}

func (p *runtimePlugin) stderrLoop(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 16*1024), 512*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		p.log.Info("stderr", "line", line)
	}
}

func (p *runtimePlugin) failPending(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, ch := range p.pending {
		ch <- protocol.Envelope{
			Type: protocol.TypeError,
			ID:   id,
			Error: &protocol.RPCError{
				Code:    "plugin_closed",
				Message: message,
			},
		}
		delete(p.pending, id)
	}
}

func (p *runtimePlugin) call(ctx context.Context, method string, params any, out any) error {
	paramBytes, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params for %s: %w", method, err)
	}
	id := fmt.Sprintf("%s-%d", p.spec.ID, atomic.AddUint64(&p.seq, 1))
	responseCh := make(chan protocol.Envelope, 1)

	env := protocol.Envelope{
		Type:   protocol.TypeCall,
		ID:     id,
		Method: method,
		Params: paramBytes,
	}

	wire, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope for %s: %w", method, err)
	}

	p.mu.Lock()
	p.pending[id] = responseCh
	_, writeErr := io.WriteString(p.stdin, string(wire)+"\n")
	p.mu.Unlock()
	if writeErr != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return fmt.Errorf("write to plugin for %s: %w", method, writeErr)
	}

	select {
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return ctx.Err()
	case env := <-responseCh:
		switch env.Type {
		case protocol.TypeError:
			if env.Error == nil {
				return fmt.Errorf("plugin returned unknown error for %s", method)
			}
			return fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		case protocol.TypeResult:
			if out == nil || len(env.Result) == 0 {
				return nil
			}
			if err := json.Unmarshal(env.Result, out); err != nil {
				return fmt.Errorf("decode result for %s: %w", method, err)
			}
			return nil
		default:
			return fmt.Errorf("unexpected envelope type for %s: %s", method, env.Type)
		}
	}
}

func (p *runtimePlugin) stop(ctx context.Context) error {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "signal: killed") {
			return err
		}
		return nil
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		return ctx.Err()
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	}
}
