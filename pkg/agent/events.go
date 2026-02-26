package agent

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	EventTypeTurnQueued      = "turn.queued"
	EventTypeTurnStarted     = "turn.started"
	EventTypeTurnCompleted   = "turn.completed"
	EventTypeTurnFailed      = "turn.failed"
	EventTypeToolCallStarted = "tool.call.started"
	EventTypeToolCallUpdated = "tool.call.updated"
	EventTypeToolCallEnded   = "tool.call.ended"
)

type Event struct {
	ID             uint64    `json:"id"`
	Type           string    `json:"type"`
	TenantID       string    `json:"tenant_id"`
	SessionID      string    `json:"session_id"`
	TurnID         string    `json:"turn_id,omitempty"`
	UserText       string    `json:"user_text,omitempty"`
	AssistantText  string    `json:"assistant_text,omitempty"`
	HandledCommand bool      `json:"handled_command,omitempty"`
	QueueDepth     int       `json:"queue_depth,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Bus interface {
	Publish(ctx context.Context, event Event)
	Subscribe(ctx context.Context, buffer int) (<-chan Event, func())
}

type EventBus struct {
	log *slog.Logger

	mu          sync.RWMutex
	subscribers map[uint64]chan Event
	seq         uint64
	subSeq      uint64
}

func NewEventBus() *EventBus {
	return &EventBus{
		log:         slog.Default().With("component", "agent_event_bus"),
		subscribers: make(map[uint64]chan Event),
	}
}

func (b *EventBus) Publish(_ context.Context, event Event) {
	if b == nil {
		return
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.ID == 0 {
		event.ID = atomic.AddUint64(&b.seq, 1)
	}

	b.mu.RLock()
	subs := make([]chan Event, 0, len(b.subscribers))
	for _, ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			if b.log != nil {
				b.log.Warn("dropping agent event for slow subscriber", "event_type", event.Type, "event_id", event.ID)
			}
		}
	}
}

func (b *EventBus) Subscribe(ctx context.Context, buffer int) (<-chan Event, func()) {
	if b == nil {
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if buffer <= 0 {
		buffer = 64
	}

	id := atomic.AddUint64(&b.subSeq, 1)
	ch := make(chan Event, buffer)

	b.mu.Lock()
	b.subscribers[id] = ch
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			existing, ok := b.subscribers[id]
			if ok {
				delete(b.subscribers, id)
			}
			b.mu.Unlock()
			if ok {
				close(existing)
			}
		})
	}

	go func() {
		<-ctx.Done()
		unsubscribe()
	}()

	return ch, unsubscribe
}
