package llm

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// readSSE reads a text/event-stream body and calls onEvent with each event's
// data payload (event name may be empty for providers that only send data
// lines). Returns on stream end or onEvent error.
func readSSE(body io.Reader, onEvent func(event, data string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var event, data string
	flush := func() error {
		if data == "" {
			event = ""
			return nil
		}
		err := onEvent(event, data)
		event, data = "", ""
		return err
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data != "" {
				data += "\n"
			}
			data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read sse stream: %w", err)
	}
	return flush()
}

// httpError turns a non-2xx provider response into a readable error,
// including a bounded slice of the body — provider error bodies are the only
// way to debug misconfigured keys and URLs.
func httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
