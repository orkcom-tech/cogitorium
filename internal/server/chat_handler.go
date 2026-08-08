package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/llm"
)

// handleChat streams a one-off chat completion as server-sent events. The
// client POSTs {model_id, messages} and reads the SSE body: events are JSON
// objects {type: "delta"|"done"|"error", ...}.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ModelID  int64 `json:"model_id"`
		Messages []struct {
			Role    string `json:"role"` // system | user | assistant
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must not be empty")
		return
	}

	req := llm.Request{}
	for _, m := range in.Messages {
		if m.Role == "system" {
			if req.System != "" {
				req.System += "\n\n"
			}
			req.System += m.Content
			continue
		}
		req.Messages = append(req.Messages, llm.Turn{Role: m.Role, Text: m.Content})
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "at least one non-system message is required")
		return
	}

	model, err := s.catalog.GetModel(r.Context(), in.ModelID)
	if err != nil {
		fail(w, r, err)
		return
	}
	client, _, err := s.catalog.Client(r.Context(), model.ProviderID)
	if err != nil {
		fail(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	send := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			slog.Error("chat: marshal sse event", "err", err)
			return
		}
		if _, err := w.Write([]byte("data: " + string(raw) + "\n\n")); err != nil {
			slog.Warn("chat: client write failed", "err", err)
			return
		}
		if err := rc.Flush(); err != nil {
			slog.Warn("chat: flush failed", "err", err)
		}
	}

	req.Model = model.ModelName
	slog.Info("chat stream started", "model_id", model.ID, "model", model.ModelName, "provider", model.ProviderName, "messages", len(req.Messages))
	res, streamErr := client.Chat(r.Context(), req, func(text string) error {
		send(map[string]string{"type": "delta", "text": text})
		return r.Context().Err()
	})
	if streamErr != nil {
		// A client that navigated away is routine, not a provider failure.
		if r.Context().Err() != nil || errors.Is(streamErr, context.Canceled) {
			slog.Info("chat stream aborted: client disconnected", "model_id", model.ID)
			return
		}
		slog.Error("chat stream failed", "model_id", model.ID, "err", streamErr)
		send(map[string]string{"type": "error", "message": streamErr.Error()})
		return
	}
	truncated := res.StopReason == "max_tokens" || res.StopReason == "length"
	if truncated {
		slog.Warn("chat stream truncated by token limit", "model_id", model.ID, "stop_reason", res.StopReason)
	}
	slog.Info("chat stream finished", "model_id", model.ID, "stop_reason", res.StopReason)
	send(map[string]any{"type": "done", "stop_reason": res.StopReason, "truncated": truncated})
}
