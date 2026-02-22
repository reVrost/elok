package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/revrost/elok/pkg/config"
)

type OpenRouterClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func NewOpenRouter(cfg config.LLMConfig) *OpenRouterClient {
	baseURL := strings.TrimSuffix(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	return &OpenRouterClient{
		baseURL: baseURL,
		apiKey:  cfg.ResolveAPIKey(),
		model:   cfg.Model,
		http: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

type openRouterRequest struct {
	Model    string              `json:"model"`
	Messages []openRouterMessage `json:"messages"`
	Stream   bool                `json:"stream,omitempty"`
}

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterStreamChunk struct {
	Choices []struct {
		Delta openRouterMessage `json:"delta"`
	} `json:"choices"`
}

func (c *OpenRouterClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
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

func (c *OpenRouterClient) Stream(ctx context.Context, req CompletionRequest) (*Stream, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, fmt.Errorf("openrouter api key is empty")
	}
	if strings.TrimSpace(c.model) == "" {
		return nil, fmt.Errorf("openrouter model is empty")
	}

	msgs := make([]openRouterMessage, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		msgs = append(msgs, openRouterMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, msg := range req.Messages {
		msgs = append(msgs, openRouterMessage{Role: msg.Role, Content: msg.Content})
	}

	payload, err := json.Marshal(openRouterRequest{
		Model:    c.model,
		Messages: msgs,
		Stream:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openrouter request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/chat/completions",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create openrouter request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("HTTP-Referer", "https://github.com/revrost/elok")
	httpReq.Header.Set("X-Title", "elok")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call openrouter: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("openrouter status %d and failed reading body: %w", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("openrouter status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	events := make(chan StreamEvent, 128)
	done := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(done)
		defer resp.Body.Close()

		emit := func(delta string) error {
			if delta == "" {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case events <- StreamEvent{Type: StreamEventTextDelta, Delta: delta}:
				return nil
			}
		}

		err := consumeSSE(ctx, resp.Body, func(event sseEvent) error {
			data := strings.TrimSpace(event.Data)
			if data == "" {
				return nil
			}
			if data == "[DONE]" {
				return io.EOF
			}

			var chunk openRouterStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return nil
			}
			for _, choice := range chunk.Choices {
				if err := emit(choice.Delta.Content); err != nil {
					return err
				}
			}
			return nil
		})

		if errors.Is(err, io.EOF) {
			done <- nil
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			done <- fmt.Errorf("openrouter stream: %w", err)
			return
		}
		done <- nil
	}()

	return &Stream{
		Events: events,
		Done:   done,
	}, nil
}
