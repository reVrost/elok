package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/revrost/elok/pkg/config"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompletionRequest struct {
	SystemPrompt     string    `json:"system_prompt"`
	Messages         []Message `json:"messages"`
	Provider         string    `json:"provider,omitempty"`
	Model            string    `json:"model,omitempty"`
	OpenRouterAPIKey string    `json:"openrouter_api_key,omitempty"`
}

type CompletionResponse struct {
	Text string `json:"text"`
}

type StreamEventType string

const (
	StreamEventTextDelta StreamEventType = "text_delta"
)

type StreamEvent struct {
	Type  StreamEventType `json:"type"`
	Delta string          `json:"delta,omitempty"`
}

type Stream struct {
	Events <-chan StreamEvent
	Done   <-chan error
}

type Client interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	Stream(ctx context.Context, req CompletionRequest) (*Stream, error)
}

func New(cfg config.LLMConfig) Client {
	return NewRouting(cfg)
}

func BuildTranscript(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		parts = append(parts, fmt.Sprintf("%s: %s", msg.Role, msg.Content))
	}
	return strings.Join(parts, "\n")
}

func CollectStreamText(ctx context.Context, stream *Stream) (string, error) {
	if stream == nil {
		return "", fmt.Errorf("llm stream is nil")
	}

	var builder strings.Builder
	events := stream.Events
	done := stream.Done

	for events != nil || done != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ev.Type == StreamEventTextDelta || ev.Type == "" {
				builder.WriteString(ev.Delta)
			}
		case err, ok := <-done:
			if !ok {
				done = nil
				continue
			}
			if err != nil {
				return "", err
			}
			done = nil
		}
	}

	return builder.String(), nil
}
