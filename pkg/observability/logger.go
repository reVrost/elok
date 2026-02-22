package observability

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/revrost/elok/pkg/config"
)

type closeFunc func() error

func (f closeFunc) Close() error {
	return f()
}

func NewLogger(cfg config.Config) (*slog.Logger, io.Closer, error) {
	level := parseLevel(cfg.Logging.Level)
	opts := &slog.HandlerOptions{Level: level}

	handlers := make([]slog.Handler, 0, 2)
	switch strings.ToLower(strings.TrimSpace(cfg.Logging.Format)) {
	case "json":
		handlers = append(handlers, slog.NewJSONHandler(os.Stdout, opts))
	default:
		handlers = append(handlers, slog.NewTextHandler(os.Stdout, opts))
	}

	var closers []io.Closer
	if strings.TrimSpace(cfg.Observability.VictoriaLogsURL) != "" {
		exportURL, err := normalizeVictoriaLogsURL(cfg.Observability.VictoriaLogsURL)
		if err != nil {
			return nil, nil, err
		}

		writer := newVictoriaLogsWriter(victoriaLogsWriterConfig{
			Endpoint:    exportURL,
			QueueSize:   cfg.Observability.VictoriaLogsQueueSize,
			FlushEvery:  time.Duration(cfg.Observability.VictoriaLogsFlushMS) * time.Millisecond,
			MaxBatchLen: cfg.Observability.VictoriaLogsBatchSize,
			HTTPClient: &http.Client{
				Timeout: time.Duration(cfg.Observability.VictoriaLogsTimeoutMS) * time.Millisecond,
			},
		})
		closers = append(closers, writer)
		handlers = append(handlers, slog.NewJSONHandler(writer, opts))
	}

	logger := slog.New(multiHandler{handlers: handlers}).With("service", "elok", "source", "core")

	return logger, closeFunc(func() error {
		var firstErr error
		for _, closer := range closers {
			if closer == nil {
				continue
			}
			if err := closer.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}), nil
}

type multiHandler struct {
	handlers []slog.Handler
}

func (h multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h multiHandler) Handle(ctx context.Context, rec slog.Record) error {
	var firstErr error
	for _, handler := range h.handlers {
		if err := handler.Handle(ctx, rec.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		out = append(out, handler.WithAttrs(attrs))
	}
	return multiHandler{handlers: out}
}

func (h multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		out = append(out, handler.WithGroup(name))
	}
	return multiHandler{handlers: out}
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func normalizeVictoriaLogsURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}

	query := parsed.Query()
	if query.Get("_stream_fields") == "" {
		query.Set("_stream_fields", "service,level")
	}
	if query.Get("_time_field") == "" {
		query.Set("_time_field", "time")
	}
	if query.Get("_msg_field") == "" {
		query.Set("_msg_field", "msg")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type victoriaLogsWriterConfig struct {
	Endpoint    string
	QueueSize   int
	FlushEvery  time.Duration
	MaxBatchLen int
	HTTPClient  *http.Client
}

type victoriaLogsWriter struct {
	cfg    victoriaLogsWriterConfig
	input  chan []byte
	closed chan struct{}

	once sync.Once
	wg   sync.WaitGroup
}

func newVictoriaLogsWriter(cfg victoriaLogsWriterConfig) *victoriaLogsWriter {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = 500 * time.Millisecond
	}
	if cfg.MaxBatchLen <= 0 {
		cfg.MaxBatchLen = 256 * 1024
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 3 * time.Second}
	}

	w := &victoriaLogsWriter{
		cfg:    cfg,
		input:  make(chan []byte, cfg.QueueSize),
		closed: make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

func (w *victoriaLogsWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	line := make([]byte, len(p))
	copy(line, p)

	select {
	case <-w.closed:
		return len(p), nil
	default:
	}

	select {
	case w.input <- line:
	default:
		// Drop when queue is full to keep logging non-blocking.
	}
	return len(p), nil
}

func (w *victoriaLogsWriter) Close() error {
	w.once.Do(func() {
		close(w.input)
		w.wg.Wait()
		close(w.closed)
	})
	return nil
}

func (w *victoriaLogsWriter) loop() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.cfg.FlushEvery)
	defer ticker.Stop()

	buffer := make([]byte, 0, w.cfg.MaxBatchLen)
	flush := func() {
		if len(buffer) == 0 {
			return
		}
		_ = w.post(buffer)
		buffer = buffer[:0]
	}

	appendLine := func(line []byte) {
		needed := len(line)
		if len(line) == 0 || line[len(line)-1] != '\n' {
			needed++
		}
		if len(buffer) > 0 && len(buffer)+needed > w.cfg.MaxBatchLen {
			flush()
		}
		buffer = append(buffer, line...)
		if len(line) == 0 || line[len(line)-1] != '\n' {
			buffer = append(buffer, '\n')
		}
	}

	for {
		select {
		case line, ok := <-w.input:
			if !ok {
				flush()
				return
			}
			appendLine(line)
		case <-ticker.C:
			flush()
		}
	}
}

func (w *victoriaLogsWriter) post(batch []byte) error {
	if len(batch) == 0 {
		return nil
	}
	req, err := http.NewRequest(http.MethodPost, w.cfg.Endpoint, bytes.NewReader(batch))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/stream+json")

	resp, err := w.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
