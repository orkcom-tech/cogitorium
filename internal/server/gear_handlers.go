package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"github.com/orkcom-tech/cogitorium/internal/gear"
)

// failGear maps gear validation errors (bad timeout, bad runtime) to 400
// rather than letting them surface as a server fault.
func failGear(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, gear.ErrNotFound) || errors.Is(err, gear.ErrConflict) {
		fail(w, r, err)
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (s *Server) handleListGears(w http.ResponseWriter, r *http.Request) {
	gears, err := s.gears.List(r.Context(), r.URL.Query().Get("tag"), r.URL.Query().Get("q"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gears)
}

// handleGetGear returns the gear plus its current version's source, so the
// operator reviews exactly what approval would make runnable.
func (s *Server) handleGetGear(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.gears.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	files, err := s.gears.Files(r.Context(), g.ID, g.Version)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gear": g, "files": files})
}

// handleCreateGear lets the operator author or correct a gear directly.
// Without it a fumbled forge is unrecoverable and a nearly-right gear
// cannot be fixed — only an agent could ever produce one.
func (s *Server) handleCreateGear(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Runtime     string   `json:"runtime"`
		Code        string   `json:"code"`
		Entrypoint  string   `json:"entrypoint"`
		ArgsSchema  string   `json:"args_schema"`
		Files       []struct {
			Path     string `json:"path"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		} `json:"files"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	files := make([]gear.File, 0, len(in.Files))
	for _, f := range in.Files {
		encoding := f.Encoding
		if encoding == "" {
			encoding = gear.EncodingUTF8
		}
		if encoding != gear.EncodingUTF8 && encoding != gear.EncodingBase64 {
			writeError(w, http.StatusBadRequest, "file encoding must be utf8 or base64")
			return
		}
		if encoding == gear.EncodingBase64 {
			if _, err := base64.StdEncoding.DecodeString(f.Content); err != nil {
				writeError(w, http.StatusBadRequest, "file "+f.Path+" is not valid base64")
				return
			}
		}
		files = append(files, gear.File{Path: f.Path, Content: f.Content, Encoding: encoding})
	}
	entrypoint := in.Entrypoint
	if in.Code != "" && len(files) == 0 {
		entrypoint = gear.DefaultEntrypoint(in.Runtime)
		files = []gear.File{{Path: entrypoint, Content: in.Code, Encoding: gear.EncodingUTF8}}
	}
	if entrypoint == "" && len(files) == 1 {
		entrypoint = files[0].Path
	}
	if in.Runtime == gear.RuntimeBinary && entrypoint == "" {
		writeError(w, http.StatusBadRequest,
			"a binary gear must say which uploaded file is the executable")
		return
	}

	// wsID/agentID 0: authored by the operator, not forged by an agent.
	g, err := s.gears.Forge(r.Context(), in.Name, in.Description, in.Tags, in.Runtime, entrypoint, in.ArgsSchema, files, 0, 0)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// handleRunGear is the dry run: execute a gear — including a pending one —
// to see what it actually does before approving it.
//
// This deliberately bypasses the approval gate, which makes it code
// execution available to any signed-in user. That is acceptable only
// because it runs in the sandbox: the blast radius is a throwaway container
// with no network and none of the server's files. Without a sandbox it is
// refused outright rather than run with the server's own access.
func (s *Server) handleRunGear(w http.ResponseWriter, r *http.Request) {
	if !s.gearExec.Sandboxed() {
		writeError(w, http.StatusForbidden,
			"dry runs need a sandbox: running unapproved code with this server's file access is refused")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Args json.RawMessage `json:"args"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	g, err := s.gears.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	args := "{}"
	if len(in.Args) > 0 {
		args = string(in.Args)
	}

	// ?stream=1 reports the run as it happens instead of at the end. The two
	// paths execute exactly the same thing and record exactly the same run —
	// the only difference is when the operator finds out. A gear with a
	// sixty-second timeout used to be a spinner, and a spinner does not say
	// whether it is thinking or hung.
	if r.URL.Query().Get("stream") == "1" {
		s.streamGearRun(w, r, g, args)
		return
	}

	res, runErr := s.gearExec.Run(r.Context(), g, args, gear.Caller{DryRun: true})
	out := map[string]any{
		"stdout": res.Stdout, "stderr": res.Stderr,
		"exit_code": res.ExitCode, "timed_out": res.TimedOut,
	}
	if runErr != nil {
		out["error"] = runErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// streamGearRun runs the gear inside this request and emits its output as it
// arrives, then one final event carrying the same result the buffered path
// would have returned.
func (s *Server) streamGearRun(w http.ResponseWriter, r *http.Request, g gear.Gear, args string) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	// The sink is called from the goroutine draining the container's pipe, and
	// this handler writes from its own — so writes are serialised rather than
	// left to race on the ResponseWriter.
	var mu sync.Mutex
	emit := func(ev map[string]any) {
		raw, err := json.Marshal(ev)
		if err != nil {
			slog.Error("gear run stream: marshal event", "err", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := w.Write([]byte("data: " + string(raw) + "\n\n")); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			slog.Warn("gear run stream: flush failed", "err", err)
		}
	}

	res, runErr := s.gearExec.RunStream(r.Context(), g, args, gear.Caller{DryRun: true},
		func(stream, chunk string) {
			emit(map[string]any{"type": "output", "stream": stream, "text": chunk})
		})

	done := map[string]any{
		"type": "result", "stdout": res.Stdout, "stderr": res.Stderr,
		"exit_code": res.ExitCode, "timed_out": res.TimedOut,
	}
	if runErr != nil {
		done["error"] = runErr.Error()
	}
	emit(done)
}

func (s *Server) handleListGearRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.gears.ListRuns(r.Context(), id, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleSetGearStatus is the operator's approval gate — the only path that
// can make agent-authored code runnable. The catalog is global, so an
// approval here reaches every agent the gear is bound to in every
// workspace; that is an administrator's decision, not a member's.
func (s *Server) handleSetGearStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status         *string `json:"status"`
		TimeoutSeconds *int    `json:"timeout_seconds"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.TimeoutSeconds != nil {
		g, err := s.gears.SetTimeout(r.Context(), id, *in.TimeoutSeconds)
		if err != nil {
			failGear(w, r, err)
			return
		}
		if in.Status == nil {
			writeJSON(w, http.StatusOK, g)
			return
		}
	}
	if in.Status == nil {
		writeError(w, http.StatusBadRequest, "nothing to change: send status and/or timeout_seconds")
		return
	}
	switch *in.Status {
	case gear.StatusPending, gear.StatusApproved, gear.StatusDisabled:
	default:
		writeError(w, http.StatusBadRequest, "status must be pending, approved or disabled")
		return
	}
	g, err := s.gears.SetStatus(r.Context(), id, *in.Status)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleDeleteGear removes a gear from the shared catalog, taking it away
// from every workspace bound to it — administrator only for the same reason
// approval is.
func (s *Server) handleDeleteGear(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.gears.Delete(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListGearBindings(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	bindings, err := s.gears.ListBindings(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bindings)
}

func (s *Server) handleCreateGearBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	var in struct {
		GearID  int64  `json:"gear_id"`
		AgentID *int64 `json:"agent_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if _, err := s.gears.Get(r.Context(), in.GearID); err != nil {
		fail(w, r, err)
		return
	}
	b, err := s.gears.Bind(r.Context(), in.GearID, id, in.AgentID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleDeleteGearBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.gears.Unbind(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
