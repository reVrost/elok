package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type ToolResult struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

type ToolHandler func(ctx context.Context, args map[string]any) (ToolResult, error)

type Tool struct {
	Def     ToolDefinition
	Handler ToolHandler
}

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *ToolRegistry {
	r := &ToolRegistry{tools: map[string]Tool{}}
	r.Register(Tool{
		Def: ToolDefinition{
			Name:        "time.now",
			Description: "Return current RFC3339 time",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		},
		Handler: func(_ context.Context, _ map[string]any) (ToolResult, error) {
			return ToolResult{Output: time.Now().UTC().Format(time.RFC3339)}, nil
		},
	})
	return r
}

func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Def.Name] = tool
}

func (r *ToolRegistry) Definitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Def)
	}
	return defs
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return ToolResult{IsError: true}, fmt.Errorf("tool not found: %s", name)
	}
	return tool.Handler(ctx, args)
}
