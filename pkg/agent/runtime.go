package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/revrost/elok/pkg/llm"
	"github.com/revrost/elok/pkg/plugins"
	"github.com/revrost/elok/pkg/store"
	"github.com/revrost/elok/pkg/tenantctx"
)

type Runtime struct {
	store              store.ChatStore
	runtimeConfigStore runtimeConfigStore
	llm                llm.Client
	plugins            *plugins.Manager
	tools              *ToolRegistry
	events             Bus
	systemPrompt       string
	log                *slog.Logger

	queueMu      sync.Mutex
	sessionQueue map[string]*sendQueue
	turnSeq      uint64
}

type sendQueue struct {
	pending []*sendRequest
	running bool
}

type sendRequest struct {
	ctx       context.Context
	tenantID  string
	sessionID string
	text      string
	provider  string
	model     string
	turnID    string
	resultCh  chan sendResponse
}

type sendResponse struct {
	result SendResult
	err    error
}

type SendResult struct {
	SessionID      string `json:"session_id"`
	UserText       string `json:"user_text"`
	AssistantText  string `json:"assistant_text"`
	HandledCommand bool   `json:"handled_command"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
}

type SendOptions struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type RuntimeLLMConfig struct {
	Provider               string `json:"provider,omitempty"`
	Model                  string `json:"model,omitempty"`
	HasOpenRouterAPIKey    bool   `json:"has_openrouter_api_key"`
	OpenRouterAPIKeyMasked string `json:"openrouter_api_key_masked,omitempty"`
}

type RuntimeLLMConfigPatch struct {
	Provider         *string `json:"provider,omitempty"`
	Model            *string `json:"model,omitempty"`
	OpenRouterAPIKey *string `json:"openrouter_api_key,omitempty"`
}

type runtimeConfigStore interface {
	GetRuntimeLLMConfig(ctx context.Context, tenantID string) (store.RuntimeLLMConfig, error)
	UpsertRuntimeLLMConfig(ctx context.Context, tenantID string, cfg store.RuntimeLLMConfig) error
}

func New(
	st store.ChatStore,
	llmClient llm.Client,
	pluginManager *plugins.Manager,
	toolRegistry *ToolRegistry) *Runtime {
	if toolRegistry == nil {
		toolRegistry = NewRegistry()
	}
	var cfgStore runtimeConfigStore
	if resolved, ok := st.(runtimeConfigStore); ok {
		cfgStore = resolved
	}
	return &Runtime{
		store:              st,
		runtimeConfigStore: cfgStore,
		llm:                llmClient,
		plugins:            pluginManager,
		tools:              toolRegistry,
		events:             NewEventBus(),
		systemPrompt:       "You are elok, a pragmatic local agent.",
		log:                slog.Default().With("component", "agent"),
		sessionQueue:       make(map[string]*sendQueue),
	}
}

func (s *Runtime) Send(ctx context.Context, sessionID, text string) (res SendResult, err error) {
	return s.SendWithOptions(ctx, sessionID, text, SendOptions{})
}

func (s *Runtime) SendWithOptions(ctx context.Context, sessionID, text string, opts SendOptions) (res SendResult, err error) {
	started := time.Now()
	defer func() {
		if s.log == nil {
			return
		}
		attrs := []any{
			"session_id", sessionID,
			"latency_ms", time.Since(started).Milliseconds(),
		}
		if err != nil {
			s.log.Warn("agent turn failed", append(attrs, "error", err.Error())...)
			return
		}
		s.log.Info("agent turn completed", append(attrs, "handled_command", res.HandledCommand)...)
	}()

	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(text) == "" {
		return SendResult{}, fmt.Errorf("text is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = newSessionID()
	}
	if err := ctx.Err(); err != nil {
		return SendResult{}, fmt.Errorf("send context: %w", err)
	}

	userText := strings.TrimSpace(text)
	tenantID := tenantctx.TenantID(ctx)
	req := &sendRequest{
		ctx:       ctx,
		tenantID:  tenantID,
		sessionID: sessionID,
		text:      userText,
		provider:  strings.TrimSpace(opts.Provider),
		model:     strings.TrimSpace(opts.Model),
		turnID:    s.nextTurnID(),
		resultCh:  make(chan sendResponse, 1),
	}
	queueDepth, startWorker := s.enqueueSendRequest(req)
	s.publishEvent(ctx, Event{
		Type:       EventTypeTurnQueued,
		TenantID:   tenantID,
		SessionID:  sessionID,
		TurnID:     req.turnID,
		UserText:   userText,
		QueueDepth: queueDepth,
	})
	if startWorker {
		go s.runLoop(tenantID, sessionID)
	}

	select {
	case out := <-req.resultCh:
		return out.result, out.err
	case <-ctx.Done():
		return SendResult{}, fmt.Errorf("wait queued turn: %w", ctx.Err())
	}
}

func (s *Runtime) EventBus() Bus {
	return s.events
}

func (s *Runtime) SubscribeEvents(ctx context.Context, buffer int) (<-chan Event, func()) {
	if s.events == nil {
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	return s.events.Subscribe(ctx, buffer)
}

func (s *Runtime) PublishEvent(ctx context.Context, event Event) {
	s.publishEvent(ctx, event)
}

func (s *Runtime) runLoop(tenantID, sessionID string) {
	queueKey := s.sessionQueueKey(tenantID, sessionID)
	for {
		req, remaining, ok := s.dequeueSendRequest(queueKey)
		if !ok {
			return
		}

		turnCtx := context.WithoutCancel(req.ctx)
		turnCtx = tenantctx.WithTenantID(turnCtx, req.tenantID)
		s.publishEvent(turnCtx, Event{
			Type:       EventTypeTurnStarted,
			TenantID:   req.tenantID,
			SessionID:  req.sessionID,
			TurnID:     req.turnID,
			UserText:   req.text,
			QueueDepth: remaining,
		})

		result, err := s.sendTurn(turnCtx, req.sessionID, req.text, SendOptions{
			Provider: req.provider,
			Model:    req.model,
		})
		if err != nil {
			s.publishEvent(turnCtx, Event{
				Type:       EventTypeTurnFailed,
				TenantID:   req.tenantID,
				SessionID:  req.sessionID,
				TurnID:     req.turnID,
				UserText:   req.text,
				QueueDepth: remaining,
				Error:      err.Error(),
			})
		} else {
			s.publishEvent(turnCtx, Event{
				Type:           EventTypeTurnCompleted,
				TenantID:       req.tenantID,
				SessionID:      result.SessionID,
				TurnID:         req.turnID,
				UserText:       req.text,
				AssistantText:  result.AssistantText,
				HandledCommand: result.HandledCommand,
				QueueDepth:     remaining,
			})
		}
		req.resultCh <- sendResponse{result: result, err: err}
	}
}

func (s *Runtime) sendTurn(ctx context.Context, sessionID, userText string, opts SendOptions) (SendResult, error) {
	tenantID := tenantctx.TenantID(ctx)
	if _, err := s.store.AppendMessage(ctx, tenantID, sessionID, "user", userText); err != nil {
		return SendResult{}, fmt.Errorf("store user message: %w", err)
	}

	if s.plugins != nil {
		handled, response, err := s.plugins.HandleCommand(ctx, sessionID, userText)
		if err != nil {
			return SendResult{}, err
		}
		if handled {
			if _, err := s.store.AppendMessage(ctx, tenantID, sessionID, "assistant", response); err != nil {
				return SendResult{}, fmt.Errorf("store command response: %w", err)
			}
			return SendResult{
				SessionID:      sessionID,
				UserText:       userText,
				AssistantText:  response,
				HandledCommand: true,
			}, nil
		}
	}

	effectiveUserText := userText
	systemPrompt := s.systemPrompt
	if s.plugins != nil {
		mutated, systemAppend, err := s.plugins.BeforeTurn(ctx, sessionID, userText)
		if err != nil {
			return SendResult{}, err
		}
		if strings.TrimSpace(mutated) != "" {
			effectiveUserText = mutated
		}
		if strings.TrimSpace(systemAppend) != "" {
			systemPrompt += "\n\n" + systemAppend
		}
	}

	messages, err := s.store.ListMessages(ctx, tenantID, sessionID, 40)
	if err != nil {
		return SendResult{}, fmt.Errorf("load session messages: %w", err)
	}
	transcript := make([]llm.Message, 0, len(messages)+1)
	for _, msg := range messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		transcript = append(transcript, llm.Message{Role: msg.Role, Content: msg.Content})
	}
	if effectiveUserText != userText {
		transcript = append(transcript, llm.Message{Role: "user", Content: effectiveUserText})
	}

	runtimeCfg, err := s.runtimeLLMConfigForTenant(ctx, tenantID)
	if err != nil {
		return SendResult{}, fmt.Errorf("load runtime llm config: %w", err)
	}

	request := llm.CompletionRequest{
		SystemPrompt:     systemPrompt,
		Messages:         transcript,
		Provider:         runtimeCfg.Provider,
		Model:            runtimeCfg.Model,
		OpenRouterAPIKey: runtimeCfg.OpenRouterAPIKey,
	}
	if strings.TrimSpace(opts.Provider) != "" {
		request.Provider = strings.TrimSpace(opts.Provider)
	}
	if strings.TrimSpace(opts.Model) != "" {
		request.Model = strings.TrimSpace(opts.Model)
	}

	response, err := s.llm.Complete(ctx, request)
	if err != nil {
		return SendResult{}, fmt.Errorf("llm complete: %w", err)
	}

	assistantText := strings.TrimSpace(response.Text)
	if assistantText == "" {
		assistantText = "(empty assistant response)"
	}
	if _, err := s.store.AppendMessage(ctx, tenantID, sessionID, "assistant", assistantText); err != nil {
		return SendResult{}, fmt.Errorf("store assistant message: %w", err)
	}

	if s.plugins != nil {
		s.plugins.AfterTurn(ctx, plugins.AfterTurnParams{
			TenantID:      tenantID,
			SessionID:     sessionID,
			UserText:      userText,
			AssistantText: assistantText,
		})
	}

	return SendResult{
		SessionID:      sessionID,
		UserText:       userText,
		AssistantText:  assistantText,
		HandledCommand: false,
		Provider:       request.Provider,
		Model:          request.Model,
	}, nil
}

func (s *Runtime) enqueueSendRequest(req *sendRequest) (queueDepth int, startWorker bool) {
	queueKey := s.sessionQueueKey(req.tenantID, req.sessionID)
	s.queueMu.Lock()
	defer s.queueMu.Unlock()

	queue, ok := s.sessionQueue[queueKey]
	if !ok {
		queue = &sendQueue{}
		s.sessionQueue[queueKey] = queue
	}
	queue.pending = append(queue.pending, req)
	if queue.running {
		return len(queue.pending), false
	}
	queue.running = true
	return len(queue.pending), true
}

func (s *Runtime) dequeueSendRequest(queueKey string) (*sendRequest, int, bool) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()

	queue, ok := s.sessionQueue[queueKey]
	if !ok || len(queue.pending) == 0 {
		if ok {
			queue.running = false
			delete(s.sessionQueue, queueKey)
		}
		return nil, 0, false
	}
	req := queue.pending[0]
	queue.pending = queue.pending[1:]
	return req, len(queue.pending), true
}

func (s *Runtime) sessionQueueKey(tenantID, sessionID string) string {
	return tenantctx.Normalize(tenantID) + ":" + sessionID
}

func (s *Runtime) nextTurnID() string {
	return fmt.Sprintf("t_%d", atomic.AddUint64(&s.turnSeq, 1))
}

func (s *Runtime) publishEvent(ctx context.Context, event Event) {
	if s.events == nil {
		return
	}
	if strings.TrimSpace(event.TenantID) == "" {
		event.TenantID = tenantctx.TenantID(ctx)
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	s.events.Publish(ctx, event)
}

func (s *Runtime) ListSessions(ctx context.Context, limit int) ([]store.Session, error) {
	return s.store.ListSessions(ctx, tenantctx.TenantID(ctx), limit)
}

func (s *Runtime) ListMessages(ctx context.Context, sessionID string, limit int) ([]store.Message, error) {
	return s.store.ListMessages(ctx, tenantctx.TenantID(ctx), sessionID, limit)
}

func (s *Runtime) ListCommandHints() []plugins.CommandDef {
	if s.plugins == nil {
		return []plugins.CommandDef{}
	}
	return s.plugins.ListCommands()
}

func (s *Runtime) GetRuntimeLLMConfig(ctx context.Context) (RuntimeLLMConfig, error) {
	tenantID := tenantctx.TenantID(ctx)
	cfg, err := s.runtimeLLMConfigForTenant(ctx, tenantID)
	if err != nil {
		return RuntimeLLMConfig{}, err
	}
	return toRuntimeLLMConfig(cfg), nil
}

func (s *Runtime) UpdateRuntimeLLMConfig(ctx context.Context, patch RuntimeLLMConfigPatch) (RuntimeLLMConfig, error) {
	if s.runtimeConfigStore == nil {
		return RuntimeLLMConfig{}, fmt.Errorf("runtime llm config store is unavailable")
	}

	tenantID := tenantctx.TenantID(ctx)
	current, err := s.runtimeConfigStore.GetRuntimeLLMConfig(ctx, tenantID)
	if err != nil {
		return RuntimeLLMConfig{}, fmt.Errorf("get runtime llm config: %w", err)
	}

	if patch.Provider != nil {
		provider, err := normalizeRuntimeProvider(*patch.Provider)
		if err != nil {
			return RuntimeLLMConfig{}, err
		}
		current.Provider = provider
	}
	if patch.Model != nil {
		current.Model = strings.TrimSpace(*patch.Model)
	}
	if patch.OpenRouterAPIKey != nil {
		current.OpenRouterAPIKey = strings.TrimSpace(*patch.OpenRouterAPIKey)
	}

	if err := s.runtimeConfigStore.UpsertRuntimeLLMConfig(ctx, tenantID, current); err != nil {
		return RuntimeLLMConfig{}, fmt.Errorf("set runtime llm config: %w", err)
	}

	updated, err := s.runtimeConfigStore.GetRuntimeLLMConfig(ctx, tenantID)
	if err != nil {
		return RuntimeLLMConfig{}, fmt.Errorf("reload runtime llm config: %w", err)
	}
	return toRuntimeLLMConfig(updated), nil
}

func (s *Runtime) runtimeLLMConfigForTenant(ctx context.Context, tenantID string) (store.RuntimeLLMConfig, error) {
	if s.runtimeConfigStore == nil {
		return store.RuntimeLLMConfig{}, nil
	}
	cfg, err := s.runtimeConfigStore.GetRuntimeLLMConfig(ctx, tenantID)
	if err != nil {
		return store.RuntimeLLMConfig{}, fmt.Errorf("runtime llm config: %w", err)
	}
	return cfg, nil
}

func normalizeRuntimeProvider(raw string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	switch provider {
	case "", "mock", "codex", "openrouter":
		return provider, nil
	default:
		return "", fmt.Errorf("unsupported provider: %s", raw)
	}
}

func toRuntimeLLMConfig(cfg store.RuntimeLLMConfig) RuntimeLLMConfig {
	key := strings.TrimSpace(cfg.OpenRouterAPIKey)
	return RuntimeLLMConfig{
		Provider:               strings.TrimSpace(cfg.Provider),
		Model:                  strings.TrimSpace(cfg.Model),
		HasOpenRouterAPIKey:    key != "",
		OpenRouterAPIKeyMasked: maskAPIKey(key),
	}
}

func maskAPIKey(raw string) string {
	key := strings.TrimSpace(raw)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "…" + key[len(key)-4:]
}

func newSessionID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "session-fallback"
	}
	return "s_" + hex.EncodeToString(buf)
}
