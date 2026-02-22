package channels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/revrost/elok/pkg/config"
)

const inboundResponseTimeout = 90 * time.Second

type Responder func(ctx context.Context, sessionID, text string) (reply string, err error)

type EnabledFunc func(cfg config.Config) bool
type StarterFunc func(ctx context.Context, cfg config.Config) error

type Status struct {
	ChannelID string     `json:"channel_id"`
	Enabled   bool       `json:"enabled"`
	Running   bool       `json:"running"`
	Connected bool       `json:"connected"`
	LastError string     `json:"last_error,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type registration struct {
	id      string
	enabled EnabledFunc
	start   StarterFunc
}

type Manager struct {
	log     *slog.Logger
	respond Responder

	mu            sync.Mutex
	registrations []registration
	closers       map[string]func() error
	statuses      map[string]Status
	started       bool
}

type inboundText struct {
	ChannelID string
	ChatID    string
	SessionID string
	Text      string
	Reply     func(ctx context.Context, text string) error
}

func NewManager(log *slog.Logger, respond Responder) *Manager {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "channels")
	m := &Manager{
		log:      log,
		respond:  respond,
		closers:  make(map[string]func() error),
		statuses: make(map[string]Status),
	}
	m.registerWhatsApp()
	return m
}

func (m *Manager) Register(id string, enabled EnabledFunc, start StarterFunc) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("channel id is required")
	}
	if enabled == nil {
		return fmt.Errorf("enabled function is required for channel %s", id)
	}
	if start == nil {
		return fmt.Errorf("start function is required for channel %s", id)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, reg := range m.registrations {
		if reg.id == id {
			return fmt.Errorf("channel %s is already registered", id)
		}
	}
	m.registrations = append(m.registrations, registration{
		id:      id,
		enabled: enabled,
		start:   start,
	})
	return nil
}

func (m *Manager) Start(ctx context.Context, cfg config.Config) error {
	if m.respond == nil {
		return fmt.Errorf("channel responder is required")
	}

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	regs := append([]registration(nil), m.registrations...)
	m.mu.Unlock()

	for _, reg := range regs {
		enabled := reg.enabled(cfg)
		m.setStatus(reg.id, func(st *Status) {
			st.Enabled = enabled
			st.Running = false
			st.Connected = false
			st.LastError = ""
		})
		if !enabled {
			continue
		}

		if err := reg.start(ctx, cfg); err != nil {
			m.setStatus(reg.id, func(st *Status) {
				st.Enabled = true
				st.Running = false
				st.Connected = false
				st.LastError = err.Error()
			})
			return fmt.Errorf("start channel %s: %w", reg.id, err)
		}

		m.setStatus(reg.id, func(st *Status) {
			st.Enabled = true
			st.Running = true
			st.LastError = ""
		})
	}

	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	regs := append([]registration(nil), m.registrations...)
	closers := make(map[string]func() error, len(m.closers))
	for k, v := range m.closers {
		closers[k] = v
	}
	m.closers = make(map[string]func() error)
	m.started = false
	m.mu.Unlock()

	var errs []error
	for i := len(regs) - 1; i >= 0; i-- {
		id := regs[i].id
		closer, ok := closers[id]
		if !ok || closer == nil {
			m.setStatus(id, func(st *Status) {
				st.Running = false
				st.Connected = false
			})
			continue
		}
		if err := closer(); err != nil {
			m.setStatus(id, func(st *Status) {
				st.Running = false
				st.Connected = false
				st.LastError = err.Error()
			})
			errs = append(errs, err)
			continue
		}
		m.setStatus(id, func(st *Status) {
			st.Running = false
			st.Connected = false
		})
	}
	return errors.Join(errs...)
}

func (m *Manager) Snapshot() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Status, 0, len(m.statuses))
	for _, st := range m.statuses {
		cloned := st
		if st.LastSeen != nil {
			ts := st.LastSeen.UTC()
			cloned.LastSeen = &ts
		}
		out = append(out, cloned)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ChannelID < out[j].ChannelID
	})
	return out
}

func (m *Manager) setCloser(channelID string, fn func() error) {
	if strings.TrimSpace(channelID) == "" || fn == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closers[channelID] = fn
}

func (m *Manager) setStatus(channelID string, mutate func(*Status)) {
	if strings.TrimSpace(channelID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.statuses[channelID]
	if st.ChannelID == "" {
		st.ChannelID = channelID
	}
	if mutate != nil {
		mutate(&st)
	}
	st.UpdatedAt = time.Now().UTC()
	m.statuses[channelID] = st
}

func (m *Manager) markConnected(channelID string, connected bool) {
	m.setStatus(channelID, func(st *Status) {
		st.Connected = connected
		st.Running = true
		if connected {
			st.LastError = ""
		}
	})
}

func (m *Manager) markDisconnected(channelID string, reason string) {
	m.setStatus(channelID, func(st *Status) {
		st.Connected = false
		st.Running = false
		if strings.TrimSpace(reason) != "" {
			st.LastError = reason
		}
	})
}

func (m *Manager) markSeen(channelID string, at time.Time) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	ts := at.UTC()
	m.setStatus(channelID, func(st *Status) {
		st.LastSeen = &ts
	})
}

func (m *Manager) onInboundText(msg inboundText) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	msgCtx, cancel := context.WithTimeout(context.Background(), inboundResponseTimeout)
	defer cancel()

	reply, err := m.respond(msgCtx, msg.SessionID, text)
	if err != nil {
		m.log.Warn("failed handling inbound message", "channel", msg.ChannelID, "chat", msg.ChatID, "error", err)
		return
	}
	reply = strings.TrimSpace(reply)
	if reply == "" || msg.Reply == nil {
		return
	}
	if err := msg.Reply(msgCtx, reply); err != nil {
		m.log.Warn("failed sending channel response", "channel", msg.ChannelID, "chat", msg.ChatID, "error", err)
	}
}
