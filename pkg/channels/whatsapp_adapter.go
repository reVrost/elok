package channels

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type whatsappOptions struct {
	StorePath      string
	Logger         *slog.Logger
	OnConnected    func()
	OnDisconnected func()
	OnLoggedOut    func(reason string)
	OnText         func(ctx context.Context, msg whatsappInboundText)
}

type whatsappInboundText struct {
	ChatID   string
	SenderID string
	PushName string
	Text     string
}

type whatsappAdapter struct {
	log            *slog.Logger
	onConnected    func()
	onDisconnected func()
	onLoggedOut    func(reason string)
	onText         func(ctx context.Context, msg whatsappInboundText)
	db             *sql.DB
	store          *sqlstore.Container
	client         *whatsmeow.Client
	handlerID      uint32
}

func newWhatsAppAdapter(ctx context.Context, opts whatsappOptions) (*whatsappAdapter, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(opts.StorePath) == "" {
		return nil, fmt.Errorf("whatsapp store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(opts.StorePath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir whatsapp store dir: %w", err)
	}

	db, err := sql.Open("sqlite3", opts.StorePath)
	if err != nil {
		return nil, fmt.Errorf("open whatsapp sqlite store: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable whatsapp foreign keys: %w", err)
	}

	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Stdout("whatsmeow/store", "INFO", true))
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("upgrade whatsapp store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("get whatsapp device: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, waLog.Stdout("whatsmeow/client", "INFO", true))
	adapter := &whatsappAdapter{
		log:            log,
		onConnected:    opts.OnConnected,
		onDisconnected: opts.OnDisconnected,
		onLoggedOut:    opts.OnLoggedOut,
		onText:         opts.OnText,
		db:             db,
		store:          container,
		client:         client,
	}
	adapter.handlerID = client.AddEventHandler(adapter.handleEvent)
	return adapter, nil
}

func (a *whatsappAdapter) Connect(ctx context.Context) error {
	if a.client == nil {
		return fmt.Errorf("whatsapp client is nil")
	}
	if a.client.Store.ID == nil {
		qrChan, err := a.client.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("get qr channel: %w", err)
		}
		go func() {
			for item := range qrChan {
				switch item.Event {
				case whatsmeow.QRChannelEventCode:
					a.log.Info("whatsapp qr code received; scan with linked devices", "code", item.Code)
				default:
					a.log.Info("whatsapp qr event", "event", item.Event)
				}
			}
		}()
	}
	if err := a.client.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect whatsapp: %w", err)
	}
	return nil
}

func (a *whatsappAdapter) SendText(ctx context.Context, chatID, text string) error {
	if a.client == nil {
		return fmt.Errorf("whatsapp client is nil")
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	jid, err := types.ParseJID(chatID)
	if err != nil {
		return fmt.Errorf("parse chat jid %q: %w", chatID, err)
	}
	_, err = a.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(trimmed)})
	if err != nil {
		return fmt.Errorf("send whatsapp message: %w", err)
	}
	return nil
}

func (a *whatsappAdapter) Close() error {
	if a.client != nil {
		a.client.RemoveEventHandler(a.handlerID)
		a.client.Disconnect()
	}
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			return fmt.Errorf("close whatsapp store container: %w", err)
		}
	}
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			return fmt.Errorf("close whatsapp sqlite: %w", err)
		}
	}
	return nil
}

func (a *whatsappAdapter) handleEvent(evt any) {
	switch event := evt.(type) {
	case *events.Connected:
		a.log.Info("whatsapp connected")
		if a.onConnected != nil {
			a.onConnected()
		}
	case *events.Disconnected:
		a.log.Info("whatsapp disconnected")
		if a.onDisconnected != nil {
			a.onDisconnected()
		}
	case *events.LoggedOut:
		a.log.Warn("whatsapp logged out", "reason", event.Reason)
		if a.onLoggedOut != nil {
			a.onLoggedOut(fmt.Sprint(event.Reason))
		}
	case *events.Message:
		a.handleMessage(event)
	}
}

func (a *whatsappAdapter) handleMessage(evt *events.Message) {
	if evt == nil || evt.Info.IsFromMe || a.onText == nil {
		return
	}
	text := extractWhatsAppText(evt.Message)
	if strings.TrimSpace(text) == "" {
		return
	}
	in := whatsappInboundText{
		ChatID:   evt.Info.Chat.String(),
		SenderID: evt.Info.Sender.String(),
		PushName: evt.Info.PushName,
		Text:     text,
	}
	go a.onText(context.Background(), in)
}

func extractWhatsAppText(message *waE2E.Message) string {
	if message == nil {
		return ""
	}
	if text := strings.TrimSpace(message.GetConversation()); text != "" {
		return text
	}
	if ext := message.GetExtendedTextMessage(); ext != nil {
		if text := strings.TrimSpace(ext.GetText()); text != "" {
			return text
		}
	}
	if image := message.GetImageMessage(); image != nil {
		if text := strings.TrimSpace(image.GetCaption()); text != "" {
			return text
		}
	}
	if video := message.GetVideoMessage(); video != nil {
		if text := strings.TrimSpace(video.GetCaption()); text != "" {
			return text
		}
	}
	if doc := message.GetDocumentMessage(); doc != nil {
		if text := strings.TrimSpace(doc.GetCaption()); text != "" {
			return text
		}
	}
	return ""
}
