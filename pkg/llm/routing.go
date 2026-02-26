package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/revrost/elok/pkg/config"
)

type RoutingClient struct {
	defaultProvider string
	clients         map[string]Client
}

func NewRouting(cfg config.LLMConfig) *RoutingClient {
	defaultProvider := normalizeProvider(cfg.Provider)
	if defaultProvider == "" {
		defaultProvider = "mock"
	}
	return &RoutingClient{
		defaultProvider: defaultProvider,
		clients: map[string]Client{
			"mock":       NewMock(cfg),
			"codex":      NewCodex(cfg),
			"openrouter": NewOpenRouter(cfg),
		},
	}
}

func (c *RoutingClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	target, routedReq, err := c.resolve(req)
	if err != nil {
		return CompletionResponse{}, err
	}
	return target.Complete(ctx, routedReq)
}

func (c *RoutingClient) Stream(ctx context.Context, req CompletionRequest) (*Stream, error) {
	target, routedReq, err := c.resolve(req)
	if err != nil {
		return nil, err
	}
	return target.Stream(ctx, routedReq)
}

func (c *RoutingClient) resolve(req CompletionRequest) (Client, CompletionRequest, error) {
	provider, model := resolveProviderAndModel(c.defaultProvider, req.Provider, req.Model)
	target, ok := c.clients[provider]
	if !ok {
		return nil, CompletionRequest{}, fmt.Errorf("unsupported provider: %s", provider)
	}

	routedReq := req
	routedReq.Provider = provider
	routedReq.Model = model
	return target, routedReq, nil
}

func resolveProviderAndModel(defaultProvider, requestedProvider, requestedModel string) (provider, model string) {
	provider = normalizeProvider(requestedProvider)
	if provider == "" {
		provider = normalizeProvider(defaultProvider)
	}
	if provider == "" {
		provider = "mock"
	}

	model = strings.TrimSpace(requestedModel)
	if prefProvider, prefModel, ok := parsePrefixedModel(model); ok {
		provider = prefProvider
		model = prefModel
	}
	return provider, model
}

func parsePrefixedModel(raw string) (provider, model string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", false
	}
	idx := strings.Index(trimmed, "#")
	if idx <= 0 {
		return "", "", false
	}
	pref := normalizeProvider(trimmed[:idx])
	if pref == "" {
		return "", "", false
	}
	return pref, strings.TrimSpace(trimmed[idx+1:]), true
}

func normalizeProvider(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "openrouter", "or":
		return "openrouter"
	case "codex", "openai":
		return "codex"
	case "mock", "native":
		return "mock"
	default:
		return ""
	}
}

var _ Client = (*RoutingClient)(nil)
