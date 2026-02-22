package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/revrost/elok/pkg/llm"
	"github.com/revrost/elok/pkg/plugins"
	"github.com/revrost/elok/pkg/store"
	"github.com/revrost/elok/pkg/tenantctx"
	"github.com/revrost/elok/pkg/tools"
)

type Service struct {
	store        store.ChatStore
	llm          llm.Client
	plugins      *plugins.Manager
	tools        *tools.Registry
	systemPrompt string
	log          *slog.Logger
}

type SendResult struct {
	SessionID      string `json:"session_id"`
	UserText       string `json:"user_text"`
	AssistantText  string `json:"assistant_text"`
	HandledCommand bool   `json:"handled_command"`
}

func NewService(st store.ChatStore, llmClient llm.Client, pluginManager *plugins.Manager, toolRegistry *tools.Registry, log *slog.Logger) *Service {
	if toolRegistry == nil {
		toolRegistry = tools.NewRegistry()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:        st,
		llm:          llmClient,
		plugins:      pluginManager,
		tools:        toolRegistry,
		systemPrompt: "You are elok, a pragmatic local agent.",
		log:          log.With("component", "agent"),
	}
}

func (s *Service) Send(ctx context.Context, sessionID, text string) (res SendResult, err error) {
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

	if strings.TrimSpace(text) == "" {
		return SendResult{}, fmt.Errorf("text is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = newSessionID()
	}
	tenantID := tenantctx.TenantID(ctx)

	userText := strings.TrimSpace(text)
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

	response, err := s.llm.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		Messages:     transcript,
	})
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
	}, nil
}

func (s *Service) ListSessions(ctx context.Context, limit int) ([]store.Session, error) {
	return s.store.ListSessions(ctx, tenantctx.TenantID(ctx), limit)
}

func (s *Service) ListMessages(ctx context.Context, sessionID string, limit int) ([]store.Message, error) {
	return s.store.ListMessages(ctx, tenantctx.TenantID(ctx), sessionID, limit)
}

func newSessionID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "session-fallback"
	}
	return "s_" + hex.EncodeToString(buf)
}
