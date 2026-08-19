package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The gear catalogue, served as a template.
//
// cog.row.gear is the name the documentation uses as its recurring override
// example, so the row is small on its own and carries the detail only when it
// is open — a list that always carried every gear's source would fetch every
// gear's source to draw a page of names.
//
// The other half of an approval is rendered beside the source, deliberately.
// An operator can only be responsible for what they can see, and "this code"
// and "what this code is given" shown apart is a decision made blind. So the
// network grant, the named values and the timeout are one form with the
// approval button in it: approving and deciding what it may reach are one act
// because they were always one decision.

func (s *Server) handleGearsPage(w http.ResponseWriter, r *http.Request) {
	s.renderGears(w, r, "", "")
}

// handleApproveGearForm is the approval, the network grant and the timeout in
// one submission, because they are one decision.
func (s *Server) handleApproveGearForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id, ok := gearID(w, r, s)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderGears(w, r, "that form could not be read", "")
		return
	}

	granted := r.PostFormValue("network") == "on"
	var hosts []string
	for _, h := range strings.Fields(strings.ReplaceAll(r.PostFormValue("hosts"), ",", " ")) {
		hosts = append(hosts, h)
	}
	if _, err := s.gears.SetNetwork(r.Context(), id, granted, hosts); err != nil {
		s.renderGears(w, r, err.Error(), "")
		return
	}
	if secs, err := strconv.Atoi(r.PostFormValue("timeout")); err == nil && secs > 0 {
		if _, err := s.gears.SetTimeout(r.Context(), id, secs); err != nil {
			s.renderGears(w, r, err.Error(), "")
			return
		}
	}

	// Approval last, so a failure earlier leaves the gear unapproved rather
	// than approved with grants nobody finished setting.
	g, err := s.gears.SetStatus(r.Context(), id, gear.StatusApproved, gearActor(r))
	if err != nil {
		s.renderGears(w, r, err.Error(), "")
		return
	}
	s.renderGears(w, r, "", fmt.Sprintf("%s v%d approved.", g.Name, g.Version))
}

func (s *Server) handleDisableGearForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id, ok := gearID(w, r, s)
	if !ok {
		return
	}
	g, err := s.gears.SetStatus(r.Context(), id, gear.StatusDisabled, gearActor(r))
	if err != nil {
		s.renderGears(w, r, err.Error(), "")
		return
	}
	s.renderGears(w, r, "", g.Name+" is switched off. Nothing calling it will run.")
}

func (s *Server) handleDeleteGearForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id, ok := gearID(w, r, s)
	if !ok {
		return
	}
	if err := s.gears.Delete(r.Context(), id); err != nil {
		s.renderGears(w, r, err.Error(), "")
		return
	}
	s.renderGears(w, r, "", "")
}

// gearActor is who is doing this, travelling WITH the change rather than
// beside it — the approval trail is written inside SetStatus for the same
// reason.
func gearActor(r *http.Request) gear.Actor {
	caller := callerFrom(r.Context())
	by := gear.Actor{Name: caller.Name}
	if caller.ID != 0 {
		id := caller.ID
		by.ID = &id
	}
	return by
}

// handleRunGearForm executes the gear now, even while pending.
//
// Open to anyone, and that is deliberate: a dry run is how somebody decides
// whether to ask an administrator to approve something, and making the
// decision require the permission would leave only administrators able to form
// an opinion. Approving stays admin-only.
func (s *Server) handleRunGearForm(w http.ResponseWriter, r *http.Request) {
	id, ok := gearID(w, r, s)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderGears(w, r, "that form could not be read", "")
		return
	}
	args := strings.TrimSpace(r.PostFormValue("args"))
	if args == "" {
		args = "{}"
	}

	g, err := s.gears.Get(r.Context(), id)
	if err != nil {
		s.renderGears(w, r, err.Error(), "")
		return
	}

	// Not streamed. The application streams this and shows output as it
	// arrives; here the answer is the finished run, which is the same bytes
	// plus the exit code. Said plainly rather than pretended: a form that
	// returned a page cannot stream, and a page that lied about streaming
	// would be worse than one that waits.
	res, err := s.gearExec.Run(r.Context(), g, args, gear.Caller{DryRun: true})
	dry := &dryRun{id: id, args: args, ran: true}
	switch {
	case err != nil:
		dry.output, dry.failed = err.Error(), true
	default:
		dry.output = strings.TrimRight(res.Stdout+res.Stderr, "\n")
		dry.failed = res.ExitCode != 0 || res.TimedOut
		if dry.output == "" {
			dry.output = "It produced no output."
		}
	}
	s.renderGearsWith(w, r, "", "", dry)
}

// dryRun is one execution somebody just asked for, carried into the render.
type dryRun struct {
	id     int64
	args   string
	ran    bool
	output string
	failed bool
}

func gearID(w http.ResponseWriter, r *http.Request, s *Server) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderGears(w, r, "that is not a gear", "")
		return 0, false
	}
	return id, true
}

func (s *Server) renderGears(w http.ResponseWriter, r *http.Request, problem, notice string) {
	s.renderGearsWith(w, r, problem, notice, nil)
}

func (s *Server) renderGearsWith(w http.ResponseWriter, r *http.Request, problem, notice string, dry *dryRun) {
	s.renderPage(w, r, "cog.page.gears", "cog.list.gears", "Gears",
		s.gearsModel(r, problem, notice, dry))
}

// gearsModel is what the page and the drawer both render. Split out so a panel
// and a page cannot drift into showing different things about the same gear.
func (s *Server) gearsModel(r *http.Request, problem, notice string, dry *dryRun) view.Gears {
	caller := callerFrom(r.Context())
	q := r.URL.Query()

	model := view.Gears{
		Ctx:      s.viewCtx(r, caller),
		IsAdmin:  caller.IsAdmin(),
		Query:    q.Get("q"),
		Tag:      q.Get("tag"),
		Error:    problem,
		Notice:   notice,
		Narrowed: q.Get("q") != "" || q.Get("tag") != "",
	}
	// Three states, not two. A server whose sandbox nobody probed is not a
	// server without one.
	if s.sandbox != nil {
		model.SandboxKnown, model.Sandboxed = true, true
	} else {
		model.SandboxKnown, model.Sandboxed = true, false
	}

	gears, err := s.gears.List(r.Context(), model.Tag, model.Query)
	if err != nil {
		model.Error = err.Error()
	}

	// Every tag, not only the ones surviving the filter: a filter you cannot
	// get out of because its option vanished is a trap.
	all, _ := s.gears.List(r.Context(), "", "")
	seen := map[string]bool{}
	for _, g := range all {
		if g.Status == gear.StatusPending {
			model.Pending++
		}
		for _, t := range g.Tags {
			seen[t] = true
		}
	}
	for t := range seen {
		model.Tags = append(model.Tags, view.Tag{Name: t, Selected: t == model.Tag})
	}
	sort.Slice(model.Tags, func(i, j int) bool { return model.Tags[i].Name < model.Tags[j].Name })

	open, _ := strconv.ParseInt(q.Get("open"), 10, 64)
	for _, g := range gears {
		row := view.Gear{
			ID: g.ID, Name: g.Name, Description: g.Description, Tags: g.Tags,
			Version: g.Version, Status: g.Status, Approved: g.Status == gear.StatusApproved,
			Runtime: g.Runtime, Entrypoint: g.Entrypoint,
			NetworkOn: g.NetworkGranted, Hosts: strings.Join(g.NetworkHosts, "\n"),
			Timeout: g.TimeoutSeconds,
		}
		// A dry run opens the row it ran in, or its own output would land on a
		// card the person cannot see.
		if dry != nil && dry.id == g.ID {
			open = g.ID
		}
		if g.ID == open && open != 0 {
			row.Open = true
			s.fillGearDetail(r, &row, g)
			if dry != nil && dry.id == g.ID {
				row.DryRan, row.DryArgs = dry.ran, dry.args
				row.DryOutput, row.DryFailed = dry.output, dry.failed
			}
		}
		model.Items = append(model.Items, row)
	}

	return model
}

// fillGearDetail reads what only an open row needs.
func (s *Server) fillGearDetail(r *http.Request, row *view.Gear, g gear.Gear) {
	row.ArgsSchema = g.ArgsSchema
	if g.Version > 1 {
		row.PreviousVersion = g.Version - 1
		row.Comparing = r.URL.Query().Get("compare") != ""
	}

	// What the last approval covered, for the comparison. Read only when
	// somebody asked: fetching every gear's previous version to draw a page
	// would double the source this screen reads for a question nobody posed.
	previous := map[string]string{}
	if row.Comparing {
		if old, err := s.gears.Files(r.Context(), g.ID, g.Version-1); err == nil {
			for _, f := range old {
				if f.Encoding != "base64" {
					previous[f.Path] = f.Content
				}
			}
		}
	}

	files, err := s.gears.Files(r.Context(), g.ID, g.Version)
	if err != nil {
		row.Files = nil
	}
	for _, f := range files {
		file := view.GearFile{Path: f.Path, Entrypoint: f.Path == g.Entrypoint}
		if f.Encoding == "base64" {
			// Its size rather than its bytes. Rendering megabytes of base64
			// helps nobody, and calling a blob reviewable source would be
			// worse than saying it cannot be read.
			file.Binary = true
			file.KB = len(f.Content) * 3 / 4 / 1024
			if raw, err := base64.StdEncoding.DecodeString(f.Content); err == nil {
				file.KB = len(raw) / 1024
			}
		} else {
			file.Content = f.Content
			if row.Comparing {
				lines, ok := view.DiffLines(previous[f.Path], f.Content)
				file.Diff, file.TooBig = lines, !ok
			}
		}
		row.Files = append(row.Files, file)
	}

	// What each named value would resolve to, at the moment of the decision.
	if len(g.EnvNames) > 0 && s.env != nil {
		statuses, err := s.env.Describe(r.Context(), nil, g.EnvNames)
		if err == nil {
			for _, st := range statuses {
				row.Grants = append(row.Grants, view.GearGrant{
					Name: st.Name, Found: st.Found, Kind: st.Kind, Source: st.Source,
				})
			}
		} else {
			for _, n := range g.EnvNames {
				row.Grants = append(row.Grants, view.GearGrant{Name: n})
			}
		}
	}

	// What it has actually done, and where it actually reached. The record
	// rather than the intention — which is the half of an approval decision
	// the source cannot show.
	if runs, err := s.gears.ListRuns(r.Context(), g.ID, gearRunsShown); err == nil {
		for _, run := range runs {
			out := strings.TrimRight(run.Stdout+run.Stderr, "\n")
			if out == "" {
				out = "It produced no output."
			}
			row.Runs = append(row.Runs, view.GearRun{
				At: run.CreatedAt, ExitCode: run.ExitCode,
				Failed: run.ExitCode != 0 || run.TimedOut, Output: out,
			})
		}
	}
	if s.gearNet != nil {
		if conns, err := s.gearNet.Store().ForGear(r.Context(), g.ID, gearRunsShown); err == nil {
			for _, c := range conns {
				row.Connections = append(row.Connections, view.GearConnection{
					Host: c.Host, Port: c.Port, Method: c.Method, State: c.State,
					Refused: strings.HasPrefix(c.State, "refused"), At: c.CreatedAt,
				})
			}
		}
	}

	approvals, err := s.gears.Approvals(r.Context(), g.ID)
	if err == nil {
		for _, a := range approvals {
			row.Approvals = append(row.Approvals, view.GearApproval{
				Version: a.Version, By: a.UserName, At: a.CreatedAt, Network: a.Network,
			})
		}
	}
}

// gearRunsShown bounds what an open row carries. A card with every run a
// popular gear ever had is a card nobody can read, and the ones worth seeing
// are the recent ones.
const gearRunsShown = 10

// handleWriteGearForm forges a gear from the form on the gears screen.
//
// This route did not exist. The form posted to /gears, Go's mux had no pattern
// for it, and the request fell through to the single-page application — which
// answered 200 with its own shell and rendered nothing, because the client has
// no /gears screen any more. From the operator's side: fill in a gear, press
// the button, land on a blank page, and no gear. The only way to author one
// was to ask an agent.
func (s *Server) handleWriteGearForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderGears(w, r, "that form could not be read", "")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	runtime := strings.TrimSpace(r.PostFormValue("runtime"))
	source := r.PostFormValue("source")

	// Named before the store is asked, because the store's own message is
	// about a gear and this one is about a form somebody is still looking at.
	switch {
	case name == "":
		s.renderGears(w, r, "a gear needs a name", "")
		return
	case strings.TrimSpace(source) == "":
		s.renderGears(w, r, "a gear needs its source — that is the thing being approved", "")
		return
	}

	entrypoint := strings.TrimSpace(r.PostFormValue("entrypoint"))
	if entrypoint == "" {
		entrypoint = gear.DefaultEntrypoint(runtime)
	}
	files := []gear.File{{Path: entrypoint, Content: source, Encoding: gear.EncodingUTF8}}

	g, err := s.gears.Forge(r.Context(), name,
		strings.TrimSpace(r.PostFormValue("description")),
		splitList(r.PostFormValue("tags")),
		runtime, entrypoint,
		strings.TrimSpace(r.PostFormValue("args_schema")),
		splitList(r.PostFormValue("env_names")),
		files, 0, 0)
	if err != nil {
		s.renderGears(w, r, err.Error(), "")
		return
	}

	// To the gear, opened, because the next thing anybody does with a gear
	// they just wrote is read it and decide about it.
	http.Redirect(w, r, fmt.Sprintf("/gears?open=%d", g.ID), http.StatusSeeOther)
}

// splitList reads a comma- or space-separated field into names.
func splitList(raw string) []string {
	var out []string
	for _, v := range strings.Fields(strings.ReplaceAll(raw, ",", " ")) {
		out = append(out, v)
	}
	return out
}
