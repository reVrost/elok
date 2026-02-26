package channels

import (
	"context"
	"fmt"
	"time"

	"github.com/revrost/elok/pkg/config"
)

const whatsappChannelID = "whatsapp"

func (m *Manager) registerWhatsApp() {
	if err := m.Register(whatsappChannelID, func(cfg config.Config) bool {
		return cfg.WhatsApp.Enabled
	}, func(ctx context.Context, cfg config.Config) error {
		return m.startWhatsApp(ctx, cfg.WhatsApp)
	}); err != nil {
		panic(err)
	}
}

func (m *Manager) startWhatsApp(ctx context.Context, cfg config.WhatsAppConfig) error {
	m.setStatus(whatsappChannelID, func(st *Status) {
		st.Enabled = true
		st.Running = true
		st.Connected = false
		st.LastError = ""
	})

	var adapter *whatsappAdapter
	var err error

	adapter, err = newWhatsAppAdapter(ctx, whatsappOptions{
		StorePath: cfg.StorePath,
		Logger:    m.log,
		OnConnected: func() {
			m.markConnected(whatsappChannelID, true)
		},
		OnDisconnected: func() {
			m.markDisconnected(whatsappChannelID, "disconnected")
		},
		OnLoggedOut: func(reason string) {
			m.markDisconnected(whatsappChannelID, fmt.Sprintf("logged out: %s", reason))
		},
		OnText: func(_ context.Context, msg whatsappInboundText) {
			m.markSeen(whatsappChannelID, time.Now().UTC())
			m.onInboundText(inboundText{
				ChannelID: whatsappChannelID,
				TenantID:  m.defaultTenantID,
				ChatID:    msg.ChatID,
				SessionID: "wa:" + msg.ChatID,
				Text:      msg.Text,
				Reply: func(ctx context.Context, text string) error {
					if adapter == nil {
						return fmt.Errorf("whatsapp adapter is nil")
					}
					return adapter.SendText(ctx, msg.ChatID, text)
				},
			})
		},
	})
	if err != nil {
		m.markDisconnected(whatsappChannelID, err.Error())
		return fmt.Errorf("init whatsapp channel: %w", err)
	}
	if err := adapter.Connect(ctx); err != nil {
		_ = adapter.Close()
		m.markDisconnected(whatsappChannelID, err.Error())
		return fmt.Errorf("connect whatsapp channel: %w", err)
	}
	m.markConnected(whatsappChannelID, true)

	m.setCloser(whatsappChannelID, func() error {
		if err := adapter.Close(); err != nil {
			return fmt.Errorf("close whatsapp channel: %w", err)
		}
		return nil
	})
	m.log.Info("channel enabled", "channel", whatsappChannelID, "store_path", cfg.StorePath)
	return nil
}
