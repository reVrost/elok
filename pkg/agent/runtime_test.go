package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/revrost/elok/pkg/llm"
	"github.com/revrost/elok/pkg/store"
	"github.com/revrost/elok/pkg/tenantctx"
	"github.com/stretchr/testify/require"
)

func TestServiceSendQueuesMessagesPerSession(t *testing.T) {
	t.Parallel()

	st := newMemoryChatStore()
	model := newBlockingLLM()
	svc := New(st, model, nil, NewRegistry())

	ctx := tenantctx.WithTenantID(context.Background(), "default")

	type sendResult struct {
		res SendResult
		err error
	}
	firstOut := make(chan sendResult, 1)
	secondOut := make(chan sendResult, 1)

	go func() {
		res, err := svc.Send(ctx, "s_queue", "first")
		firstOut <- sendResult{res: res, err: err}
	}()
	require.Equal(t, "first", <-model.started)

	go func() {
		res, err := svc.Send(ctx, "s_queue", "second")
		secondOut <- sendResult{res: res, err: err}
	}()

	select {
	case started := <-model.started:
		t.Fatalf("unexpected concurrent llm call start: %s", started)
	case <-time.After(80 * time.Millisecond):
	}

	model.release <- struct{}{}
	first := <-firstOut
	require.NoError(t, first.err)
	require.Equal(t, "s_queue", first.res.SessionID)
	require.Equal(t, "reply:first", first.res.AssistantText)

	require.Equal(t, "second", <-model.started)
	model.release <- struct{}{}
	second := <-secondOut
	require.NoError(t, second.err)
	require.Equal(t, "reply:second", second.res.AssistantText)

	messages, err := svc.ListMessages(ctx, "s_queue", 10)
	require.NoError(t, err)
	require.Len(t, messages, 4)
	require.Equal(t, "user", messages[0].Role)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "assistant", messages[1].Role)
	require.Equal(t, "reply:first", messages[1].Content)
	require.Equal(t, "user", messages[2].Role)
	require.Equal(t, "second", messages[2].Content)
	require.Equal(t, "assistant", messages[3].Role)
	require.Equal(t, "reply:second", messages[3].Content)
}

func TestServiceSendPublishesTurnEvents(t *testing.T) {
	t.Parallel()

	st := newMemoryChatStore()
	model := newBlockingLLM()
	svc := New(st, model, nil, NewRegistry())

	events, unsubscribe := svc.SubscribeEvents(context.Background(), 32)
	defer unsubscribe()

	type sendResult struct {
		res SendResult
		err error
	}
	out := make(chan sendResult, 1)
	ctx := tenantctx.WithTenantID(context.Background(), "default")
	go func() {
		res, err := svc.Send(ctx, "s_events", "hello")
		out <- sendResult{res: res, err: err}
	}()

	require.Equal(t, "hello", <-model.started)
	model.release <- struct{}{}
	result := <-out
	require.NoError(t, result.err)
	require.Equal(t, "reply:hello", result.res.AssistantText)

	var seen []Event
	deadline := time.After(2 * time.Second)
	for len(seen) < 3 {
		select {
		case ev, ok := <-events:
			require.True(t, ok)
			if ev.SessionID != "s_events" {
				continue
			}
			seen = append(seen, ev)
		case <-deadline:
			t.Fatalf("timed out waiting for turn events, seen=%d", len(seen))
		}
	}

	require.Equal(t, EventTypeTurnQueued, seen[0].Type)
	require.Equal(t, EventTypeTurnStarted, seen[1].Type)
	require.Equal(t, EventTypeTurnCompleted, seen[2].Type)
	require.Equal(t, "hello", seen[2].UserText)
	require.Equal(t, "reply:hello", seen[2].AssistantText)
}

type blockingLLM struct {
	started chan string
	release chan struct{}
}

func newBlockingLLM() *blockingLLM {
	return &blockingLLM{
		started: make(chan string, 8),
		release: make(chan struct{}, 8),
	}
}

func (c *blockingLLM) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	last := ""
	if len(req.Messages) > 0 {
		last = req.Messages[len(req.Messages)-1].Content
	}
	select {
	case c.started <- last:
	case <-ctx.Done():
		return llm.CompletionResponse{}, ctx.Err()
	}

	select {
	case <-c.release:
	case <-ctx.Done():
		return llm.CompletionResponse{}, ctx.Err()
	}
	return llm.CompletionResponse{Text: "reply:" + last}, nil
}

func (c *blockingLLM) Stream(_ context.Context, _ llm.CompletionRequest) (*llm.Stream, error) {
	return nil, fmt.Errorf("stream is not implemented in blockingLLM")
}

type memoryChatStore struct {
	mu       sync.Mutex
	nextID   int64
	messages []store.Message
	sessions map[string]store.Session
}

func newMemoryChatStore() *memoryChatStore {
	return &memoryChatStore{
		sessions: make(map[string]store.Session),
	}
}

func (s *memoryChatStore) AppendMessage(_ context.Context, tenantID, sessionID, role, content string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	now := time.Now().UTC()
	msg := store.Message{
		ID:        s.nextID,
		TenantID:  tenantID,
		SessionID: sessionID,
		Role:      role,
		Content:   content,
		CreatedAt: now,
	}
	s.messages = append(s.messages, msg)

	key := tenantID + ":" + sessionID
	session := s.sessions[key]
	if session.ID == "" {
		session = store.Session{
			TenantID:  tenantID,
			ID:        sessionID,
			CreatedAt: now,
		}
	}
	session.UpdatedAt = now
	session.LastMessageAt = now
	s.sessions[key] = session

	return msg.ID, nil
}

func (s *memoryChatStore) ListSessions(_ context.Context, tenantID string, limit int) ([]store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}
	out := make([]store.Session, 0, limit)
	for _, session := range s.sessions {
		if session.TenantID != tenantID {
			continue
		}
		out = append(out, session)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryChatStore) ListMessages(_ context.Context, tenantID, sessionID string, limit int) ([]store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 100
	}
	filtered := make([]store.Message, 0, limit)
	for _, msg := range s.messages {
		if msg.TenantID != tenantID || msg.SessionID != sessionID {
			continue
		}
		filtered = append(filtered, msg)
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return append([]store.Message(nil), filtered...), nil
}
