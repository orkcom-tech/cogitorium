package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strconv"

	"github.com/orkcom-tech/cogitorium/internal/view"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The transcript: the conversation as it is replayed to the model every turn.
//
// WHAT IS AND IS NOT CONVERTED, because the line matters.
//
// What is here is the SETTLED conversation. A turn in flight is a stream of
// deltas the client paints as they arrive, and it stays there deliberately —
// not because streaming HTML is impossible, but because a template renders a
// thing that exists and a half-written sentence does not exist yet. When the
// turn lands, its message is a row like every other one, so there is exactly
// one rendering of a message anybody can point at or override.
//
// The event stream keeps its shape. It is a POST that answers with SSE, which
// no EventSource can open, so making it hypermedia would mean splitting the
// send from the stream and giving the engine a per-workspace channel to
// publish into. That is a real restructure of the product's most load-bearing
// path, and it is not the price of making a message overridable.

func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	wsID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "that is not a workspace", http.StatusBadRequest)
		return
	}
	s.renderDrawer(w, r, "cog.stage.chat", func() any { return s.transcriptModel(r, wsID) })
}

// handleForgetMessageForm drops one entry from the conversation.
//
// It is replayed to the model on every turn, so forgetting is the only way to
// stop something wrong steering every answer after it.
func (s *Server) handleForgetMessageForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "that is not a message", http.StatusBadRequest)
		return
	}
	wsID, err := s.workspaces.WorkspaceOfMessage(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := s.workspaces.DeleteMessage(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderDrawer(w, r, "cog.stage.chat", func() any { return s.transcriptModel(r, wsID) })
}

func (s *Server) transcriptModel(r *http.Request, wsID int64) view.Transcript {
	model := view.Transcript{Ctx: s.viewCtx(r, callerFrom(r.Context()))}

	agents, _ := s.workspaces.ListAgents(r.Context(), wsID)
	name := map[int64]string{}
	for _, a := range agents {
		name[a.ID] = a.Name
	}

	// The whole conversation, because the whole conversation is what is
	// replayed to the model — a transcript showing less than the model is
	// told is a transcript that cannot explain the answer.
	messages, err := s.workspaces.ListMessages(r.Context(), wsID, nil, 0)
	if err != nil {
		model.Error = err.Error()
		return model
	}
	model.Empty = len(messages) == 0

	for _, m := range messages {
		row := view.ChatMessage{
			ID: m.ID, Kind: m.Kind, Content: m.Content, At: m.CreatedAt,
			IsUser:       m.Kind == "user",
			IsAssistant:  m.Kind == "assistant",
			IsToolResult: m.Kind == "tool_result",
			IsDelegation: m.Kind == "delegation",
		}
		if m.AgentID != nil {
			row.Who = name[*m.AgentID]
			// Only a delegate is named. The orchestrator is the voice of the
			// workspace, so labelling every one of its replies is the same
			// noise as labelling your own.
			row.Named = row.Who != "" && row.Who != "orchestrator"
		}
		decorate(&row, m)

		// An assistant turn with neither words nor a tool call has nothing to
		// show, and an empty bubble reads as a failure rather than as a turn
		// that only delegated.
		if row.IsAssistant && row.Content == "" && len(row.Calls) == 0 {
			continue
		}
		model.Messages = append(model.Messages, row)
	}
	return model
}

// decorate reads the meta a message carries.
//
// Display-only, so a meta that will not parse costs the row its decoration and
// never the conversation: a transcript that refuses to render because one
// entry has a malformed field is a transcript nobody can read at all.
func decorate(row *view.ChatMessage, m workspace.Message) {
	if m.Meta == "" {
		return
	}
	var meta struct {
		Attachments []struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
			Kind  string `json:"kind"`
		} `json:"attachments"`
		ToolCalls []struct {
			Name string `json:"name"`
			N    string `json:"Name"`
		} `json:"tool_calls"`
		Name    string `json:"name"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(m.Meta), &meta); err != nil {
		return
	}

	for _, a := range meta.Attachments {
		att := view.Attachment{
			Name: path.Base(a.Path), Bytes: humanBytes(a.Bytes),
			// A file the model cannot read goes to a gear as a path instead.
			// Saying so is the difference between "it ignored my file" and
			// "it handed it on".
			ToGear: a.Kind == "",
		}
		att.Title = a.Path
		if att.ToGear {
			att.Title = a.Path + " — the model cannot read this, so it is given to gears as a path"
		}
		row.Attachments = append(row.Attachments, att)
	}
	for _, c := range meta.ToolCalls {
		n := c.Name
		if n == "" {
			n = c.N
		}
		if n == "" {
			n = "?"
		}
		row.Calls = append(row.Calls, view.ToolCall{Name: n})
	}
	if row.IsToolResult {
		row.ToolName, row.ToolFailed = meta.Name, meta.IsError
		if row.ToolName == "" {
			row.ToolName = "tool"
		}
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// terminalModel is the gate around a shell.
//
// The same refusal the status endpoint gives, in the same words: an operator
// who opens a terminal and finds nothing learns only that something is broken,
// and the reason is the part they can act on.
func (s *Server) terminalModel(r *http.Request) view.Terminal {
	model := view.Terminal{Ctx: s.viewCtx(r, callerFrom(r.Context()))}
	if model.Reason = s.terminalRefusal(); model.Reason != "" {
		return model
	}
	model.Available = true
	// Which shell this is, so the panel can say it. See view.Terminal.Host.
	model.Host = s.onThisMachine(true)
	return model
}
