package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/revrost/elok/pkg/agent"
	"github.com/revrost/elok/pkg/channels"
	"github.com/revrost/elok/pkg/tenantctx"
	elokui "github.com/revrost/elok/ui"
)

type Server struct {
	addr            string
	log             *slog.Logger
	agent           *agent.Service
	channels        *channels.Manager
	defaultTenantID string
	http            *http.Server
	uiFS            fs.FS
	ui              http.Handler
	seq             uint64
}

func NewServer(addr string, svc *agent.Service, channelManager *channels.Manager, defaultTenantID string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "gateway")

	var uiFS fs.FS
	var uiHandler http.Handler
	distFS, err := elokui.DistFS()
	if err != nil {
		log.Warn("ui dist is unavailable; only API routes are served", "error", err)
	} else {
		uiFS = distFS
		uiHandler = http.FileServer(http.FS(distFS))
	}

	mux := http.NewServeMux()
	s := &Server{
		addr:            addr,
		log:             log,
		agent:           svc,
		channels:        channelManager,
		defaultTenantID: tenantctx.Normalize(defaultTenantID),
		uiFS:            uiFS,
		ui:              uiHandler,
		http: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/status/channels", s.handleChannelStatus)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/", s.handleUI)
	return s
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("gateway listening", "addr", s.addr)
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleChannelStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"channels": s.channelStatusSnapshot(),
	})
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil || s.uiFS == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	target := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if target == "." || target == "" {
		target = "index.html"
	} else {
		if _, err := fs.Stat(s.uiFS, target); err != nil {
			target = "index.html"
		}
	}

	clone := cloneRequestWithPath(r, "/"+target)
	s.ui.ServeHTTP(w, clone)
}

func cloneRequestWithPath(r *http.Request, targetPath string) *http.Request {
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = targetPath
	clone.URL = &urlCopy
	return clone
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Warn("ws accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	for {
		var req Envelope
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			if websocket.CloseStatus(err) != -1 {
				return
			}
			s.log.Debug("ws read ended", "error", err)
			return
		}
		if req.Type != EnvelopeTypeCall {
			continue
		}
		requestID := s.normalizeRequestID(req.ID)
		if req.ID == "" {
			req.ID = requestID
		}
		reqLog := s.log.With("request_id", requestID, "method", req.Method)
		started := time.Now()
		resp := s.handleCall(ctx, req, reqLog)
		latency := time.Since(started).Milliseconds()
		if resp.Type == EnvelopeTypeError {
			code := ""
			if resp.Error != nil {
				code = resp.Error.Code
			}
			reqLog.Warn("gateway call failed", "code", code, "latency_ms", latency)
		} else {
			reqLog.Debug("gateway call completed", "latency_ms", latency)
		}
		if err := wsjson.Write(ctx, conn, resp); err != nil {
			s.log.Debug("ws write failed", "error", err)
			return
		}
	}
}

func (s *Server) handleCall(ctx context.Context, req Envelope, reqLog *slog.Logger) Envelope {
	switch req.Method {
	case "system.ping":
		return resultEnvelope(req.ID, map[string]any{"ok": true, "ts": time.Now().UTC().Format(time.RFC3339)})
	case "system.channels":
		return resultEnvelope(req.ID, map[string]any{"channels": s.channelStatusSnapshot()})
	case "session.send":
		var in SessionSendParams
		if err := decodeParams(req.Params, &in); err != nil {
			return errorEnvelope(req.ID, "bad_params", err.Error())
		}
		callCtx, err := s.scopedCallContext(ctx, in.TenantID)
		if err != nil {
			return errorEnvelope(req.ID, "tenant_unsupported", err.Error())
		}
		res, err := s.agent.Send(callCtx, in.SessionID, in.Text)
		if err != nil {
			if reqLog != nil {
				reqLog.Warn("session.send failed", "session_id", in.SessionID, "error", err)
			}
			return errorEnvelope(req.ID, "send_failed", err.Error())
		}
		if reqLog != nil {
			reqLog.Info("session.send completed", "session_id", res.SessionID, "handled_command", res.HandledCommand)
		}
		return resultEnvelope(req.ID, SessionSendResult{
			SessionID:      res.SessionID,
			AssistantText:  res.AssistantText,
			HandledCommand: res.HandledCommand,
		})
	case "session.list":
		var in SessionListParams
		if len(req.Params) > 0 {
			if err := decodeParams(req.Params, &in); err != nil {
				return errorEnvelope(req.ID, "bad_params", err.Error())
			}
		}
		callCtx, err := s.scopedCallContext(ctx, in.TenantID)
		if err != nil {
			return errorEnvelope(req.ID, "tenant_unsupported", err.Error())
		}
		sessions, err := s.agent.ListSessions(callCtx, in.Limit)
		if err != nil {
			return errorEnvelope(req.ID, "list_failed", err.Error())
		}
		return resultEnvelope(req.ID, map[string]any{"sessions": sessions})
	case "session.messages":
		var in SessionMessagesParams
		if err := decodeParams(req.Params, &in); err != nil {
			return errorEnvelope(req.ID, "bad_params", err.Error())
		}
		if in.SessionID == "" {
			return errorEnvelope(req.ID, "bad_params", "session_id is required")
		}
		callCtx, err := s.scopedCallContext(ctx, in.TenantID)
		if err != nil {
			return errorEnvelope(req.ID, "tenant_unsupported", err.Error())
		}
		messages, err := s.agent.ListMessages(callCtx, in.SessionID, in.Limit)
		if err != nil {
			return errorEnvelope(req.ID, "messages_failed", err.Error())
		}
		return resultEnvelope(req.ID, map[string]any{"messages": messages})
	default:
		return errorEnvelope(req.ID, "method_not_found", fmt.Sprintf("unknown method: %s", req.Method))
	}
}

func (s *Server) normalizeRequestID(id string) string {
	if id != "" {
		return id
	}
	next := atomic.AddUint64(&s.seq, 1)
	return fmt.Sprintf("gw-%d", next)
}

func (s *Server) channelStatusSnapshot() []ChannelStatus {
	if s.channels == nil {
		return []ChannelStatus{}
	}
	raw := s.channels.Snapshot()
	out := make([]ChannelStatus, 0, len(raw))
	for _, st := range raw {
		item := ChannelStatus{
			ChannelID: st.ChannelID,
			Enabled:   st.Enabled,
			Running:   st.Running,
			Connected: st.Connected,
			LastError: st.LastError,
			UpdatedAt: st.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if st.LastSeen != nil {
			item.LastSeen = st.LastSeen.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out
}

func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	return nil
}

func (s *Server) scopedCallContext(ctx context.Context, requestedTenantID string) (context.Context, error) {
	requested := tenantctx.Normalize(requestedTenantID)
	if requested != s.defaultTenantID {
		// TODO(multi-tenant): replace this with auth-scoped tenant resolution and per-tenant authorization.
		return nil, fmt.Errorf("tenant_id %q is unavailable in single-tenant mode", requested)
	}
	return tenantctx.WithTenantID(ctx, s.defaultTenantID), nil
}

func resultEnvelope(id string, result any) Envelope {
	data, _ := json.Marshal(result)
	return Envelope{Type: EnvelopeTypeResult, ID: id, Result: data}
}

func errorEnvelope(id, code, message string) Envelope {
	return Envelope{
		Type: EnvelopeTypeError,
		ID:   id,
		Error: &EnvelopeError{
			Code:    code,
			Message: message,
		},
	}
}
