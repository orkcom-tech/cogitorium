package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/llm"
)

// handleChat streams a one-off chat completion as server-sent events. The
// client POSTs {model_id, messages} and reads the SSE body: events are JSON
// objects {type: "delta"|"done"|"error", ...}.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ModelID  int64         `json:"model_id"`
		Messages []llm.Message `json:"messages"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must not be empty")
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

	slog.Info("chat stream started", "model_id", model.ID, "model", model.ModelName, "provider", model.ProviderName, "messages", len(in.Messages))
	streamErr := client.Stream(r.Context(), model.ModelName, in.Messages, func(text string) error {
		send(map[string]string{"type": "delta", "text": text})
		return r.Context().Err()
	})
	if streamErr != nil {
		slog.Error("chat stream failed", "model_id", model.ID, "err", streamErr)
		send(map[string]string{"type": "error", "message": streamErr.Error()})
		return
	}
	slog.Info("chat stream finished", "model_id", model.ID)
	send(map[string]string{"type": "done"})
}
