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
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
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
	// ?version=N reads an older revision's source. Every version's files are
	// kept — an approval covers exact content, so the content it covered must
	// stay retrievable — and this is what makes "what changed between v1 and
	// v2" answerable rather than merely stored.
	version := g.Version
	if raw := r.URL.Query().Get("version"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > g.Version {
			writeError(w, http.StatusBadRequest, "version must be a number between 1 and this gear's current version")
			return
		}
		version = n
	}
	files, err := s.gears.Files(r.Context(), g.ID, version)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The named values go out beside the source, in the same response, because
	// this is the request the approval screen makes. An operator can only be
	// responsible for what they can see, and "this code" and "these
	// credentials" shown apart is a decision made blind.
	//
	// Statuses, never values: whether this install can supply each name and
	// which source would win. A gear naming something nothing supplies is a
	// gear that cannot run, and learning that at approval beats learning it at
	// three in the morning.
	env, err := s.env.Describe(r.Context(), nil, g.EnvNames)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The network grant travels on the gear itself, so it is already here. This
	// response is deliberately the whole approval screen in one request: the
	// source, the credentials this code would be given, and whether it may reach
	// out and where. The plan is explicit that shown apart, the decision is made
	// blind — and two requests that can fail independently is a way of showing
	// them apart.
	writeJSON(w, http.StatusOK, map[string]any{"gear": g, "files": files, "version": version, "env": env})
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
		EnvNames    []string `json:"env_names"`
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
	g, err := s.gears.Forge(r.Context(), in.Name, in.Description, in.Tags, in.Runtime, entrypoint, in.ArgsSchema, in.EnvNames, files, 0, 0)
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
// with none of the server's files, and no network unless one is asked for
// below — which is an administrator's request, because it is half of that
// radius. Without a sandbox it is refused outright rather than run with the
// server's own access.
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
		// The grant the operator is CONSIDERING, so that "try it before
		// approving it" still works for a gear whose whole job is to fetch
		// something. Without this the dry run is the one thing a network gear
		// cannot be judged by: the grant does not exist until approval, and
		// approval is the decision the dry run exists to inform.
		Network *networkInput `json:"network"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	g, err := s.gears.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Network != nil && in.Network.Granted {
		// A dry run is open to any signed-in user because the sandbox is the
		// blast radius — a throwaway container holding none of this server's
		// files and, until here, no network at all. Handing it one takes half of
		// that away, so this half is the administrator's, exactly like the
		// approval it is standing in for.
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
		hosts, err := gearnet.NormalizeHosts(in.Network.Hosts)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Applied to the copy this run uses and never written down: nothing is
		// granted by trying it. The connections it makes are recorded like any
		// other, which is the point — the log is part of what the operator is
		// deciding on.
		g.NetworkGranted, g.NetworkHosts = true, hosts
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

// handleInvokeGear runs an APPROVED gear, as the operator, and refuses
// anything else.
//
// This is not handleRunGear with a different name. That one is the dry run: it
// deliberately bypasses the approval gate so an operator can see what code
// does before trusting it, and it is safe only because a dry run is a throwaway
// container and an operator act.
//
// This one is the opposite promise. It exists so a gear can be called from
// outside a conversation — the CLI, an MCP client — and the whole value of
// that is that the approval gate still holds. It sets no DryRun, so the
// executor's own check refuses a pending or disabled gear, and the refusal
// comes back as 403 rather than as a run that quietly happened.
func (s *Server) handleInvokeGear(w http.ResponseWriter, r *http.Request) {
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

	// No Caller fields at all: not a dry run, and not on behalf of an agent.
	// The connection log names the operator by the absence of an agent, which
	// is what distinguishes "somebody ran this from a shell" from "an agent
	// called it mid-turn".
	res, runErr := s.gearExec.Run(r.Context(), g, args, gear.Caller{})
	if errors.Is(runErr, gear.ErrNotApproved) {
		writeError(w, http.StatusForbidden, runErr.Error())
		return
	}
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

// networkInput is the operator's answer to "may this code reach out, and
// where". An empty Hosts with Granted true is anywhere, and that is a choice
// the operator is allowed to make — the list is what makes the connection log
// auditable afterwards, not a gate on them.
type networkInput struct {
	Granted bool     `json:"granted"`
	Hosts   []string `json:"hosts"`
}

// handleSetGearStatus is the operator's approval gate — the only path that
// can make agent-authored code runnable. The catalog is global, so an
// approval here reaches every agent the gear is bound to in every
// workspace; that is an administrator's decision, not a member's.
//
// The network grant arrives through the same route and in the same request as
// the approval, which is not tidiness: it is one decision. The screen shows the
// source, the credentials and the destinations together, and this is what that
// screen submits — approving with one request and granting with a second would
// leave a window in which the code is runnable and the operator has not
// finished deciding what it may do.
func (s *Server) handleSetGearStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status         *string       `json:"status"`
		TimeoutSeconds *int          `json:"timeout_seconds"`
		Network        *networkInput `json:"network"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Status == nil && in.TimeoutSeconds == nil && in.Network == nil {
		writeError(w, http.StatusBadRequest, "nothing to change: send status, timeout_seconds and/or network")
		return
	}
	if in.Status != nil {
		switch *in.Status {
		case gear.StatusPending, gear.StatusApproved, gear.StatusDisabled:
		default:
			writeError(w, http.StatusBadRequest, "status must be pending, approved or disabled")
			return
		}
	}

	// Ordered so that the gear is never runnable for an instant under a grant
	// the operator has not finished making: what it may do is written first,
	// and the approval that lets it run at all is written last.
	var g gear.Gear
	var err error
	if in.TimeoutSeconds != nil {
		if g, err = s.gears.SetTimeout(r.Context(), id, *in.TimeoutSeconds); err != nil {
			failGear(w, r, err)
			return
		}
	}
	if in.Network != nil {
		if g, err = s.gears.SetNetwork(r.Context(), id, in.Network.Granted, in.Network.Hosts); err != nil {
			failGear(w, r, err)
			return
		}
	}
	if in.Status != nil {
		if g, err = s.gears.SetStatus(r.Context(), id, *in.Status); err != nil {
			fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, g)
}

// handleListGearConnections is the other half of the execution history: what
// this gear's code reached, when, and whether the grant let it.
//
// Behind the same rule as the run history rather than a stricter one — a run
// record carries the gear's stdout, which is a great deal more than a list of
// host names, so a tighter gate here would guard the smaller thing.
func (s *Server) handleListGearConnections(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	conns, err := s.gearNet.Store().ForGear(r.Context(), id, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, conns)
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
	// Read it before deleting it: the row is the only thing that knows the
	// gear's name, and the name is what its directory on disk is called.
	g, err := s.gears.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := s.gears.Delete(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	// After the row, and never fatal to the request: the gear IS deleted, and
	// a 500 here would invite the operator to delete it again.
	if err := s.gearExec.RemoveFiles(g.Name); err != nil {
		slog.Warn("gear deleted but its files could not be removed", "gear_id", id, "gear", g.Name, "err", err)
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
