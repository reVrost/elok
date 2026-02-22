package llm

import (
	"bufio"
	"context"
	"io"
	"strings"
)

type sseEvent struct {
	Event string
	Data  string
}

func consumeSSE(ctx context.Context, reader io.Reader, onEvent func(event sseEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var eventName string
	dataLines := make([]string, 0, 8)

	flush := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		event := sseEvent{
			Event: eventName,
			Data:  strings.Join(dataLines, "\n"),
		}
		eventName = ""
		dataLines = dataLines[:0]
		return onEvent(event)
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}

		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
