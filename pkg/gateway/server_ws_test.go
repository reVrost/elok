package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/revrost/elok/pkg/agent"
	"github.com/revrost/elok/pkg/llm"
	"github.com/revrost/elok/pkg/store"
	"github.com/stretchr/testify/require"
)

func TestHandleWSRelaysAgentEventsAndQueuesSessionSends(t *testing.T) {
	t.Parallel()

	st := newGatewayMemoryStore()
	model := newGatewayBlockingLLM()
	agentSvc := agent.New(st, model, nil, agent.NewRegistry())
	server := NewServer(":0", agentSvc, nil, "default")

	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	readerErr := make(chan error, 1)
	inbound := make(chan Envelope, 64)
	go func() {
		for {
			var env Envelope
			if err := wsjson.Read(ctx, conn, &env); err != nil {
				readerErr <- err
				return
			}
			inbound <- env
		}
	}()

	require.NoError(t, wsjson.Write(ctx, conn, Envelope{
		Type:   EnvelopeTypeCall,
		ID:     "req-1",
		Method: "session.send",
		Params: mustRawMessage(t, SessionSendParams{
			SessionID: "s_ws",
			Text:      "first",
		}),
	}))
	require.NoError(t, wsjson.Write(ctx, conn, Envelope{
		Type:   EnvelopeTypeCall,
		ID:     "req-2",
		Method: "session.send",
		Params: mustRawMessage(t, SessionSendParams{
			SessionID: "s_ws",
			Text:      "second",
		}),
	}))

	firstStarted := <-model.started
	require.Contains(t, []string{"first", "second"}, firstStarted)
	select {
	case started := <-model.started:
		t.Fatalf("second llm turn started before first completed: %s", started)
	case <-time.After(80 * time.Millisecond):
	}

	model.release <- struct{}{}
	secondStarted := <-model.started
	require.Contains(t, []string{"first", "second"}, secondStarted)
	require.NotEqual(t, firstStarted, secondStarted)
	model.release <- struct{}{}

	results := make(map[string]SessionSendResult)
	var sessionEvents []agent.Event

	deadline := time.After(3 * time.Second)
	for {
		if len(results) == 2 && len(sessionEvents) >= 3 {
			break
		}
		select {
		case err := <-readerErr:
			t.Fatalf("websocket reader failed: %v", err)
		case env := <-inbound:
			switch env.Type {
			case EnvelopeTypeResult:
				var out SessionSendResult
				require.NoError(t, json.Unmarshal(env.Result, &out))
				results[env.ID] = out
			case EnvelopeTypeEvent:
				var event agent.Event
				require.NoError(t, json.Unmarshal(env.Data, &event))
				if event.SessionID == "s_ws" {
					sessionEvents = append(sessionEvents, event)
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for results/events: got_results=%d got_events=%d", len(results), len(sessionEvents))
		}
	}

	require.Contains(t, results, "req-1")
	require.Contains(t, results, "req-2")
	require.Equal(t, "reply:first", results["req-1"].AssistantText)
	require.Equal(t, "reply:second", results["req-2"].AssistantText)

	var eventTypes []string
	for _, ev := range sessionEvents {
		eventTypes = append(eventTypes, ev.Type)
	}
	require.Contains(t, eventTypes, agent.EventTypeTurnQueued)
	require.Contains(t, eventTypes, agent.EventTypeTurnStarted)
	require.Contains(t, eventTypes, agent.EventTypeTurnCompleted)
}

func mustRawMessage(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

type gatewayBlockingLLM struct {
	started chan string
	release chan struct{}
}

func newGatewayBlockingLLM() *gatewayBlockingLLM {
	return &gatewayBlockingLLM{
		started: make(chan string, 8),
		release: make(chan struct{}, 8),
	}
}

func (c *gatewayBlockingLLM) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
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

func (c *gatewayBlockingLLM) Stream(_ context.Context, _ llm.CompletionRequest) (*llm.Stream, error) {
	return nil, fmt.Errorf("stream is not implemented in gatewayBlockingLLM")
}

type gatewayMemoryStore struct {
	mu       sync.Mutex
	nextID   int64
	messages []store.Message
	sessions map[string]store.Session
}

func newGatewayMemoryStore() *gatewayMemoryStore {
	return &gatewayMemoryStore{
		sessions: make(map[string]store.Session),
	}
}

func (s *gatewayMemoryStore) AppendMessage(_ context.Context, tenantID, sessionID, role, content string) (int64, error) {
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

func (s *gatewayMemoryStore) ListSessions(_ context.Context, tenantID string, limit int) ([]store.Session, error) {
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

func (s *gatewayMemoryStore) ListMessages(_ context.Context, tenantID, sessionID string, limit int) ([]store.Message, error) {
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

var _ store.ChatStore = (*gatewayMemoryStore)(nil)
var _ llm.Client = (*gatewayBlockingLLM)(nil)
