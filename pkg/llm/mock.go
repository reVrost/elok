package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/revrost/elok/pkg/config"
)

type MockClient struct {
	model string
}

func NewMock(cfg config.LLMConfig) *MockClient {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "mock/default"
	}
	return &MockClient{model: model}
}

func (c *MockClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	stream, err := c.Stream(ctx, req)
	if err != nil {
		return CompletionResponse{}, err
	}
	text, err := CollectStreamText(ctx, stream)
	if err != nil {
		return CompletionResponse{}, err
	}
	return CompletionResponse{Text: text}, nil
}

func (c *MockClient) Stream(_ context.Context, req CompletionRequest) (*Stream, error) {
	events := make(chan StreamEvent, 2)
	done := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(done)

		text := "No messages yet."
		if len(req.Messages) > 0 {
			last := req.Messages[len(req.Messages)-1]
			content := strings.TrimSpace(last.Content)
			if content == "" {
				content = "(empty)"
			}
			text = fmt.Sprintf("[%s] %s", c.model, content)
		}

		events <- StreamEvent{Type: StreamEventTextDelta, Delta: text}
		done <- nil
	}()

	return &Stream{
		Events: events,
		Done:   done,
	}, nil
}
