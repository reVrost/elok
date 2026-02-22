package tools

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type Result struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

type Handler func(ctx context.Context, args map[string]any) (Result, error)

type Tool struct {
	Def     Definition
	Handler Handler
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	r := &Registry{tools: map[string]Tool{}}
	r.Register(Tool{
		Def: Definition{
			Name:        "time.now",
			Description: "Return current RFC3339 time",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		},
		Handler: func(_ context.Context, _ map[string]any) (Result, error) {
			return Result{Output: time.Now().UTC().Format(time.RFC3339)}, nil
		},
	})
	return r
}

func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Def.Name] = tool
}

func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]Definition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Def)
	}
	return defs
}

func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (Result, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return Result{IsError: true}, fmt.Errorf("tool not found: %s", name)
	}
	return tool.Handler(ctx, args)
}
