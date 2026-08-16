package gear

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

const (
	defaultTimeout = 60 * time.Second
	maxOutputSize  = 64 * 1024
)

// Executor materializes a gear's approved source into the data dir and runs
// its entrypoint as a subprocess.
//
// Isolation is a subprocess with a timeout and a dedicated working
// directory — the gear runs with the server's own privileges. The real
// control is the operator approval gate, not the sandbox; container-level
// isolation is deliberately out of scope for the MVP and tracked as the
// known limitation it is.
type Executor struct {
	store   *Store
	baseDir string
	// dataDir is kept whole, not just the gears directory under it, because a
	// call that carries files reads and writes the workspace's own directory —
	// and where that is is workdir's to say, not this package's.
	dataDir string
	// sandbox, when set, runs gears isolated from the server's files. It is
	// what stops a gear from reading the database and lifting the provider
	// API keys out of it — a subprocess cannot be stopped from that, since
	// it holds the server's own filesystem access.
	sandbox sandbox.Runner
	// env resolves the names a gear declared into the values to inject. nil
	// means this server has no such source at all, and a gear that declares a
	// name is then refused rather than run without it.
	env *secrets.Resolver
	// browserImage is what the "browser" environment resolves to. Held here
	// rather than read from a package variable so an install that configured a
	// different one is the one that decides, and a test can use an image it
	// actually has.
	browserImage string
	// gate carries the traffic of a gear the operator granted the network, and
	// records every connection. nil means this server has no gate, and a
	// granted gear is then refused rather than run with an unrecorded network.
	gate *gearnet.Gate
}

func NewExecutor(store *Store, dataDir string, sb sandbox.Runner, env *secrets.Resolver, gate *gearnet.Gate) *Executor {
	if sb == nil {
		slog.Warn("gears will run as unsandboxed subprocesses with this server's file access — " +
			"an approved gear can read the database, including provider API keys; " +
			"install Docker or set sandbox: docker to isolate them")
	}
	return &Executor{
		store:        store,
		baseDir:      filepath.Join(dataDir, "gears"),
		dataDir:      dataDir,
		sandbox:      sb,
		env:          env,
		gate:         gate,
		browserImage: config.DefaultBrowserImage,
	}
}

// Backend names how gears are currently executed, so the operator can see
// whether approval is their only protection.
func (e *Executor) Backend() string {
	if e.sandbox == nil {
		return "subprocess (not isolated)"
	}
	return e.sandbox.Name()
}

// Sandboxed reports whether gear execution is isolated from the server's
// files. Anything that runs code without the approval gate must check this
// first.
func (e *Executor) Sandboxed() bool { return e.sandbox != nil && e.sandbox.Isolated() }

type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	// Produced is what the gear left in out/, and where each file landed in the
	// workspace. Nil for a call that named no files — such a call has no out/
	// to leave anything in.
	Produced []Produced `json:"produced,omitempty"`
	// Ignored names what was in out/ and did not come back, and why. A gear's
	// output going missing without a word is the failure this exists to
	// prevent; the caller is told, in the same breath as what did arrive.
	Ignored []string `json:"ignored,omitempty"`
}

// interpreter returns the command that runs a gear's entrypoint. A binary
// gear has none — it is executed directly.
func interpreter(runtime string) (string, []string) {
	switch runtime {
	case "python":
		return "python3", nil
	case "node":
		return "node", nil
	case RuntimeBinary:
		return "", nil
	default:
		return "bash", nil
	}
}

// command builds the argv for a gear: interpreter plus entrypoint, or the
// entrypoint alone when it is an executable.
func command(g Gear) (string, []string) {
	bin, preArgs := interpreter(g.Runtime)
	if bin == "" {
		return "./" + g.Entrypoint, nil
	}
	return bin, append(append([]string{}, preArgs...), g.Entrypoint)
}

// Caller identifies who a run is on behalf of. A nil AgentID means the
// operator running a dry run from the catalog.
type Caller struct {
	AgentID     *int64
	WorkspaceID *int64
	// AgentName is carried so the connection log can name who a request was
	// made for. The id alone is not enough: that log has no foreign key, on
	// purpose, so a row outlives the agent — and SQLite recycles rowids, so an
	// id read a month later may belong to somebody else entirely.
	AgentName string
	// DryRun lets the operator execute a gear that is not yet approved —
	// so that approval is an informed decision rather than a leap of faith.
	// Agents never set this.
	DryRun bool
}

// Run executes a gear with argsJSON delivered on stdin, records the
// execution, and refuses anything the operator has not approved (except an
// operator's own dry run).
func (e *Executor) Run(ctx context.Context, g Gear, argsJSON string, caller Caller) (Result, error) {
	return e.RunStream(ctx, g, argsJSON, caller, nil)
}

// RunWithFiles is Run with the file protocol switched on: the workspace-relative
// paths in files are placed in the run's in/ directory, an empty out/ is
// created beside it, and whatever the gear leaves in out/ is moved into the
// workspace and reported in the Result.
//
// A nil files is not the same as an empty one, and the difference is the whole
// of the compatibility promise. Nil means the caller said nothing about files:
// no in/, no out/, and a run byte-for-byte identical to the one this gear has
// always had. Empty means the caller asked for the protocol without handing
// anything over — a gear that produces a file out of its arguments alone still
// needs somewhere to put it.
func (e *Executor) RunWithFiles(ctx context.Context, g Gear, argsJSON string, files []string, caller Caller) (Result, error) {
	return e.run(ctx, g, argsJSON, files, caller, nil)
}

// RunStream is Run with a tap on the output.
//
// onOutput, when set, is called with each chunk as the gear produces it. The
// recorded run is identical either way — the audit trail must not depend on
// whether anyone happened to be watching — so this only decides whether the
// operator sees a sixty-second gear working or a spinner followed by
// everything at once.
//
// An agent's tool call passes nil: there is nobody at the other end of it, and
// a turn's transcript already carries the result.
func (e *Executor) RunStream(ctx context.Context, g Gear, argsJSON string, caller Caller, onOutput func(stream, chunk string)) (Result, error) {
	return e.run(ctx, g, argsJSON, nil, caller, onOutput)
}

// run is the one path every gear execution takes. The two backends differ in
// how they start a process and in nothing else: the approval gate, the run
// directory, the file protocol, the recorded run and the truncation are all
// here, so neither backend can quietly grow its own version of any of them.
func (e *Executor) run(ctx context.Context, g Gear, argsJSON string, files []string, caller Caller, onOutput func(stream, chunk string)) (Result, error) {
	if g.Status != StatusApproved && !caller.DryRun {
		return Result{}, fmt.Errorf("gear %q (status %s): %w — the operator must approve it in the gear catalog first", g.Name, g.Status, ErrNotApproved)
	}
	if caller.DryRun && caller.AgentID != nil {
		// Defence in depth: a dry run is an operator act by definition.
		return Result{}, errors.New("a dry run cannot be performed on behalf of an agent")
	}

	// Whether this machine can run the gear at all, asked before anything is
	// materialised: a directory rebuilt for a run that cannot happen is a side
	// effect with nothing to show for it, and it is also what keeps "nothing
	// ran" out of the recorded runs below.
	if e.sandbox == nil {
		if bin, _ := interpreter(g.Runtime); bin == "" {
			// A binary gear is executed directly, and the subprocess path has no
			// interpreter to hand it to. It was already refused here, with a
			// message that read "needs , which is not installed".
			return Result{}, fmt.Errorf("gear %q is a %s gear, and this server has no sandbox to run one in: "+
				"install Docker or set sandbox: docker", g.Name, RuntimeBinary)
		} else if _, err := exec.LookPath(bin); err != nil {
			return Result{}, fmt.Errorf("gear %q needs %s, which is not installed on this machine", g.Name, bin)
		}
	}

	// The network is opened first, and the order is load-bearing rather than
	// tidy: a granted run's secrets become stand-ins minted ON the ticket, so
	// the ticket has to exist before the environment is resolved. Both are
	// before anything is materialised, for the same reason the interpreter is
	// checked there — a run that cannot be given what it was granted has not
	// run, and must not appear in the audit trail as one that did. Closing the
	// ticket cuts every connection still open, and voids every stand-in it
	// minted.
	ticket, err := e.openNetwork(g, caller)
	if err != nil {
		return Result{}, err
	}
	defer ticket.Close()

	env, red, err := e.resolveEnv(ctx, g, caller, ticket)
	if err != nil {
		return Result{}, err
	}

	dir, fc, cleanup, err := e.prepare(ctx, g, files, caller)
	if err != nil {
		return Result{}, err
	}
	if err := e.trustGate(dir, ticket, env); err != nil {
		cleanup()
		return Result{}, err
	}
	defer cleanup()

	timeout := defaultTimeout
	if g.TimeoutSeconds > 0 {
		timeout = time.Duration(g.TimeoutSeconds) * time.Second
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}

	// The live tap is wrapped BEFORE the run, not the output cleaned after it:
	// a watching operator's screen is a place a secret would otherwise reach
	// ahead of the redacted record. flushTap releases the tail the wrapper holds
	// back while it waits to see whether a secret straddles two chunks.
	onOutput, flushTap := red.Tap(onOutput)

	var (
		res     Result
		elapsed time.Duration
		runErr  error
	)
	// From here a process was started, or was attempted with everything needed
	// to start it present — so everything below records a run that happened.
	if e.sandbox != nil {
		res, elapsed, runErr = e.execSandboxed(ctx, g, dir, argsJSON, timeout, env, ticket, fc, onOutput)
	} else {
		res, elapsed, runErr = e.execSubprocess(ctx, g, dir, argsJSON, timeout, env, ticket, onOutput)
	}
	flushTap()

	// Everything that leaves this function passes through here, and this is the
	// only place it does: the agent's tool result, the operator's stream, the
	// recorded run, the log lines below and the error itself all read these
	// fields. A gear that prints its own credential — by accident, in a stack
	// trace, or on purpose — gets [redacted] in all of them.
	//
	// It cannot stop the gear from SENDING the value somewhere; nothing can,
	// and the plan says so. It stops the value from being published by us.
	if red.Active() {
		res.Stdout, res.Stderr = red.String(res.Stdout), red.String(res.Stderr)
		runErr = red.Error(runErr)
	}

	// Collected whatever happened: a gear that wrote its result and then exited
	// non-zero still wrote its result.
	if fc != nil {
		produced, ignored, cerr := collectOut(dir, fc.root, fc.destRel)
		res.Produced, res.Ignored = produced, ignored
		// File names go into the chat and into the record exactly as stdout
		// does, and a gear chooses them. A gear that writes out/<the key>.json
		// would otherwise publish the key in a field nobody thought to check.
		if red.Active() {
			for i := range res.Produced {
				res.Produced[i].Name = red.String(res.Produced[i].Name)
				res.Produced[i].Path = red.String(res.Produced[i].Path)
			}
			res.Ignored = red.Strings(res.Ignored)
		}
		if cerr != nil {
			// Redacted for the same reason the names above are: this message
			// quotes the paths the gear chose.
			cerr = red.Error(cerr)
			// Attached to the result as well as returned, so the caller learns
			// what happened to its files even when a run error takes priority.
			res.Ignored = append(res.Ignored, cerr.Error())
			if runErr == nil {
				runErr = cerr
			} else {
				slog.Warn("could not collect a gear's output", "gear", g.Name, "err", cerr)
			}
		}
		// Dereferenced: prepare has already refused a file call without a
		// workspace, and slog prints a *int64 as the pointer it is.
		slog.Info("gear produced files", "gear", g.Name, "workspace_id", *caller.WorkspaceID,
			"produced", len(res.Produced), "ignored", len(res.Ignored), "landed_in", fc.destRel)
	}

	slog.Info("gear executed", "gear", g.Name, "version", g.Version, "backend", e.Backend(),
		"exit_code", res.ExitCode, "timed_out", res.TimedOut, "dry_run", caller.DryRun,
		"network", g.NetworkGranted, "duration_ms", elapsed.Milliseconds())

	// Recorded even when the caller's context is done: an execution that
	// happened must appear in the operator's audit trail.
	if err := e.store.RecordRun(context.WithoutCancel(ctx), Run{
		GearID: g.ID, Version: g.Version, AgentID: caller.AgentID, WorkspaceID: caller.WorkspaceID,
		Args: argsJSON, ExitCode: res.ExitCode, TimedOut: res.TimedOut,
		DurationMs: elapsed.Milliseconds(), Stdout: res.Stdout, Stderr: res.Stderr,
	}); err != nil {
		slog.Error("could not record gear run", "gear", g.Name, "err", err)
	}
	return res, runErr
}

// resolveEnv turns the names a gear declared into the values to inject, and
// into the redactor that keeps those values out of everything the run produces.
//
// The sandbox is given EXACTLY these names and nothing else — the server's own
// environment is never inherited, so a gear sees the credentials it was
// approved to hold and no others.
//
// A name that does not resolve stops the run, naming it. The alternative, an
// empty string, fails later and elsewhere: inside somebody's HTTP client, with
// a 401 and no hint that the credential was never there.
func (e *Executor) resolveEnv(ctx context.Context, g Gear, caller Caller, ticket *gearnet.Ticket) (map[string]string, *secrets.Redactor, error) {
	if len(g.EnvNames) == 0 {
		return nil, nil, nil
	}
	// A dry run gets NO named values, secret or otherwise.
	//
	// The dry run exists so an operator can execute a gear BEFORE approving it,
	// which means it is the one path that runs code nobody has agreed to. Handing
	// it the install's credentials makes the approval decorative: an agent forges
	// a gear that declares API_KEY and prints it, the operator presses the button
	// that is meant to be the safe way to look, and the key is out — past the
	// redactor, since the gear can encode it however it likes.
	//
	// So the dry run answers the question it is for — does this code run, what
	// does it do with its arguments — and says plainly that the values are not
	// there. A gear that cannot be judged without a live credential is a gear
	// whose approval is a judgement about the source, which is what the operator
	// is reading at that moment anyway.
	if caller.DryRun {
		slog.Info("dry run: named values withheld", "gear", g.Name, "names", g.EnvNames)
		env := make(map[string]string, len(g.EnvNames))
		for _, name := range g.EnvNames {
			env[name] = ""
		}
		return env, nil, nil
	}
	if e.env == nil {
		return nil, nil, fmt.Errorf("gear %q asks to be given %v, and this server has no store of named values to resolve them from — "+
			"this is a build or configuration fault rather than something the gear did", g.Name, g.EnvNames)
	}
	values, err := e.env.Resolve(ctx, caller.WorkspaceID, g.EnvNames)
	if err != nil {
		return nil, nil, fmt.Errorf("gear %q cannot run: %w", g.Name, err)
	}

	env := make(map[string]string, len(values))
	var toRedact []string
	sources := make([]string, 0, len(values))
	var referenced int
	for _, v := range values {
		env[v.Name] = v.Value
		if v.IsSecret() {
			// The real value is still redacted from everything this software
			// shows, whether or not a stand-in was minted: a gear given the
			// real value must not print it, and a gear given a stand-in may
			// still be handed the real one by a destination that echoes it.
			toRedact = append(toRedact, v.Value)
			// A secret becomes a stand-in when there is an edge to substitute
			// it at. That edge is the gate, and a run without the network has
			// none — so a gear that was not granted the network gets the real
			// value, exactly as before, because a stand-in it could never
			// exchange for anything would just be a broken credential.
			if ticket != nil {
				if ref := ticket.Reference(v.Value); ref != v.Value {
					env[v.Name] = ref
					referenced++
				}
			}
		}
		sources = append(sources, v.Name+" from "+v.Source)
	}
	// Which names, from which source, for whom — and no values. This is the
	// operator's record of a credential having been handed to running code.
	slog.Info("gear given named values", "gear", g.Name, "version", g.Version,
		"names", g.EnvNames, "resolved", sources, "secrets", len(toRedact),
		"as_references", referenced)
	return env, secrets.NewRedactor(toRedact...), nil
}

// gateCAFile is where a run finds the certificate it must trust when the gate
// reads inside its TLS. Hidden, and inside the payload, because that is the one
// directory a gear can certainly read in every backend.
const gateCAFile = ".gearnet-ca.crt"

// trustGate hands the run the gate's certificate, when and only when the gate
// will be terminating its TLS.
//
// Without this the substitution would be perfect and useless: the gear's own
// client would see a certificate it does not trust and refuse the connection —
// which is the client behaving correctly, and a failure that reads as the
// destination's problem rather than as this install's design.
//
// The variables are the ones the ecosystem actually reads, and there is no one
// of them: curl and Go take SSL_CERT_FILE, requests takes REQUESTS_CA_BUNDLE,
// node takes NODE_EXTRA_CA_CERTS. Setting one and calling it done would work in
// whichever language the author of the test happened to use.
func (e *Executor) trustGate(dir string, ticket *gearnet.Ticket, env map[string]string) error {
	if !ticket.Intercepts() || env == nil {
		return nil
	}
	pem := e.gate.CACert()
	if len(pem) == 0 {
		return fmt.Errorf("this run holds secret references, so the gate has to read inside its TLS, and this " +
			"server has no certificate to be trusted with — a build or configuration fault rather than " +
			"something the gear did")
	}
	if err := os.WriteFile(filepath.Join(dir, gateCAFile), pem, 0o644); err != nil {
		return fmt.Errorf("hand the run the gate's certificate: %w", err)
	}
	// Where the file is from INSIDE the run: a sandbox sees the payload at
	// /work, a subprocess sees it where it is on this machine.
	at := filepath.Join(dir, gateCAFile)
	if e.sandbox != nil {
		at = "/work/" + gateCAFile
	}
	for _, name := range []string{
		"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
		"NODE_EXTRA_CA_CERTS", "GIT_SSL_CAINFO",
	} {
		env[name] = at
	}
	return nil
}

// SetBrowserImage points the "browser" environment at this install's image.
func (e *Executor) SetBrowserImage(image string) {
	if image != "" {
		e.browserImage = image
	}
}

// imageFor resolves the gear's granted environment to an image, or to empty —
// which the backend reads as its ordinary one.
func (e *Executor) imageFor(g Gear) string {
	if g.Environment == EnvironmentBrowser {
		return e.browserImage
	}
	return ""
}

// openNetwork turns the operator's grant into a ticket for this run, or into a
// refusal naming what is missing. A gear that was not granted the network gets
// no ticket, and nil is a working ticket in every sense that matters here: it
// adds no environment, and closing it does nothing.
//
// The grant is never inferred and never widened. A gear reaches the network
// because an operator ticked a box while reading its source, or it does not
// reach the network.
func (e *Executor) openNetwork(g Gear, caller Caller) (*gearnet.Ticket, error) {
	if !g.NetworkGranted {
		return nil, nil
	}
	if e.gate == nil {
		return nil, fmt.Errorf("gear %q was granted the network and this server has no gate to carry it through, "+
			"so it was not run — this is a build or configuration fault rather than something the gear did", g.Name)
	}
	t, err := e.gate.Open(gearnet.Grant{
		GearID: g.ID, GearName: g.Name, Version: g.Version,
		WorkspaceID: caller.WorkspaceID, AgentID: caller.AgentID, AgentName: caller.AgentName,
		Hosts: g.NetworkHosts,
	})
	if err != nil {
		return nil, fmt.Errorf("gear %q cannot run: %w", g.Name, err)
	}
	// The grant, at the moment it is used, in the operator's own words. "anywhere"
	// is said out loud rather than shown as an empty list, because an empty list
	// in a log reads as "nothing" and means the opposite.
	where := strings.Join(g.NetworkHosts, ", ")
	if where == "" {
		where = "anywhere"
	}
	slog.Info("gear given the network", "gear", g.Name, "version", g.Version,
		"destinations", where, "workspace_id", caller.WorkspaceID, "agent", caller.AgentName)
	return t, nil
}

// withEnv adds the resolved names to the fixed environment a gear always gets.
//
// The fixed entries are written LAST so they cannot be displaced: a declared
// name can never be PATH or HOME (secrets.ValidName refuses those, and every
// COGITORIUM_ name), and writing them in this order means that even if that
// rule were ever loosened the sandbox's own environment would still win.
// fixedEnv is the environment the sandbox itself owns: the entries above plus,
// for a granted run, the proxy the gate hands out. hostname is how this run
// reaches the gate — a container has to name the host it is running on, a
// subprocess is already on it.
//
// It goes in the FIXED half rather than beside the resolved names on purpose:
// withEnv writes fixed last, so a gear cannot point its own HTTP_PROXY
// somewhere the connection log does not see.
func fixedEnv(base map[string]string, ticket *gearnet.Ticket, hostname string) map[string]string {
	if ticket != nil {
		maps.Copy(base, ticket.Env(hostname))
	}
	return base
}

func withEnv(fixed map[string]string, resolved map[string]string) map[string]string {
	out := make(map[string]string, len(fixed)+len(resolved))
	for k, v := range resolved {
		out[k] = v
	}
	for k, v := range fixed {
		out[k] = v
	}
	return out
}

// fileCall is what a call that carries files needs to remember for afterwards:
// whose workspace it belongs to, and where in it the gear's output goes.
type fileCall struct {
	root    string // the workspace's directory on this machine
	destRel string // where out/ lands, relative to the workspace root
}

// RemoveFiles deletes what a gear left on disk: the directory named after it
// under <data>/gears.
//
// Every run now builds and removes its own directory below this one, so in a
// quiet install what is left is an empty directory per gear name. Deleting the
// gear should take that with it — otherwise <data>/gears accumulates a
// directory for every gear that has ever existed, and the one that fills a disk
// is the run directory of a gear killed mid-execution, which nothing else will
// ever come back for.
//
// A missing directory is success. A gear that was never run has nothing here.
func (e *Executor) RemoveFiles(name string) error {
	if name == "" {
		return errors.New("a gear with no name has no directory to remove")
	}
	dir := filepath.Join(e.baseDir, name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove the gear directory %s: %w", dir, err)
	}
	return nil
}

// prepare builds the directory the gear runs in, and returns the cleanup that
// must follow it.
//
// EVERY run gets a directory of its own: <data>/gears/<name>/v<version>.run-<token>,
// built from the database and removed afterwards.
//
// It did not always. A call carrying no files ran in the shared
// <data>/gears/<name>/v<version>, and materialize starts by deleting whatever is
// there — so a second run of the same gear wiped the first one's directory
// while it was still executing. Two agents in two workspaces calling one gear at
// the same moment was already possible, and this comment already said so; the
// private directory was given only to the calls carrying files, on the grounds
// that one caller's in/ must not appear in another's. The reason for a private
// directory turns out to be the smaller half of the reason for one.
//
// Removing it afterwards costs the rebuild on the next call, which happens on
// every call regardless: materialize rebuilds from the database each time, so
// that a deleted-and-reforged gear reusing a name and version number can never
// execute stale files under the new gear's approval.
func (e *Executor) prepare(ctx context.Context, g Gear, files []string, caller Caller) (string, *fileCall, func(), error) {
	noop := func() {}
	token, err := runToken()
	if err != nil {
		return "", nil, noop, err
	}
	dir := filepath.Join(e.baseDir, g.Name, fmt.Sprintf("v%d.run-%s", g.Version, token))
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("could not remove a gear run directory", "gear", g.Name, "dir", dir, "err", err)
		}
	}
	if err := e.materialize(ctx, g, dir); err != nil {
		cleanup()
		return "", nil, noop, err
	}
	if files == nil {
		return dir, nil, cleanup, nil
	}

	if caller.WorkspaceID == nil {
		cleanup()
		return "", nil, noop, errors.New("a gear can only be given files on behalf of a workspace, and this run has none — " +
			"an operator's dry run from the catalog has no workspace to take files from or hand them back to")
	}
	root := workdir.Dir(e.dataDir, *caller.WorkspaceID)
	if root == "" {
		cleanup()
		return "", nil, noop, fmt.Errorf("workspace %d has no working directory on this machine, so it has no files to give", *caller.WorkspaceID)
	}
	given, err := stageIn(root, dir, files)
	if err != nil {
		cleanup()
		return "", nil, noop, err
	}
	for _, f := range given {
		// One line per file, not a count: the operator's question about a gear
		// run is always "what did it get to see", and a number does not answer
		// it. The recorded run holds the arguments and the output; this is the
		// only place the files appear.
		slog.Info("gear given a workspace file", "gear", g.Name, "workspace_id", *caller.WorkspaceID,
			"path", f.from, "as", f.at, "bytes", f.size)
	}
	// 0755 rather than 0700: with the Docker backend the tar rewrites this
	// anyway, but on the unsandboxed path the mode is all there is, and a gear
	// that cannot create a file in out/ has nowhere to put its answer.
	if err := os.MkdirAll(filepath.Join(dir, outDir), 0o755); err != nil {
		cleanup()
		return "", nil, noop, fmt.Errorf("create the run's out/ directory: %w", err)
	}
	return dir, &fileCall{root: root, destRel: path.Join(workdir.GearOutDir, g.Name, token)}, cleanup, nil
}

// execSubprocess runs the gear as a child of this server, with this server's
// file access. There is no isolation here and no pretending otherwise: in/ is
// written 0444 so an honest gear that opens it for writing fails, and that is a
// courtesy, not a boundary — the process runs as the account that owns those
// files and can chmod them back. The boundary is the sandbox, which is why
// NewExecutor says so out loud when there isn't one.
func (e *Executor) execSubprocess(ctx context.Context, g Gear, dir, argsJSON string, timeout time.Duration, env map[string]string, ticket *gearnet.Ticket, onOutput func(stream, chunk string)) (Result, time.Duration, error) {
	// The interpreter is known to exist: run checked before materialising, so
	// that "this machine cannot run this gear" is not recorded as a run.
	bin, preArgs := interpreter(g.Runtime)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append(append([]string{}, preArgs...), g.Entrypoint)
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(argsJSON)
	var stdout, stderr bytes.Buffer
	// The same tap the sandboxed path has. Without it, live output silently
	// did nothing on a server with no Docker — the sink was accepted and
	// dropped, which is worse than not offering it.
	cmd.Stdout = tap(&stdout, "stdout", onOutput)
	cmd.Stderr = tap(&stderr, "stderr", onOutput)
	// Kill the whole process group on timeout, and stop waiting on pipes an
	// orphan is holding open. See procgroup_unix.go: without this a gear that
	// backgrounds anything outlives its timeout AND blocks the call forever,
	// which on this path means blocking the turn that called it.
	// afterStart exists for Windows, where a process can only be put into its
	// job object once it exists — see procgroup_windows.go. On Unix the group
	// is established at fork and both hooks are empty.
	afterStart, releaseGroup := isolateProcess(cmd)
	defer releaseGroup()
	cmd.WaitDelay = 3 * time.Second
	// Keep the environment minimal: a forged tool has no business reading
	// the server's environment (which may hold provider API keys). What the
	// gear DECLARED is added to that minimum, and nothing else is — the
	// server's own variables are still not inherited here.
	//
	// A note this path has to make, because it is the one place the guarantee is
	// weaker: there is no --network none for a subprocess. It holds the server's
	// network exactly as it holds the server's files, granted or not. The proxy
	// is still given to a granted gear so its traffic is checked and recorded
	// like everyone else's; for an ungranted one, the sandbox is what would have
	// stopped it, and NewExecutor already says out loud that there isn't one.
	cmd.Env = envList(withEnv(fixedEnv(map[string]string{
		"PATH":            os.Getenv("PATH"),
		"HOME":            dir,
		"COGITORIUM_GEAR": g.Name,
	}, ticket, ""), env))

	// Start and Wait rather than Run: the containment hook has to land between
	// the two, and Run does not offer a seam.
	start := time.Now()
	runErr := cmd.Start()
	if runErr == nil {
		afterStart()
		runErr = cmd.Wait()
	}
	elapsed := time.Since(start)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	res := Result{
		Stdout:   truncate(stdout.String()),
		Stderr:   truncate(stderr.String()),
		ExitCode: exitCode,
		TimedOut: runCtx.Err() == context.DeadlineExceeded,
	}
	if res.TimedOut {
		return res, elapsed, fmt.Errorf("gear %q timed out after %s", g.Name, timeout)
	}
	if runErr != nil && res.ExitCode == 0 {
		// Failed to start at all (not a non-zero exit).
		return res, elapsed, fmt.Errorf("gear %q could not run: %w", g.Name, runErr)
	}
	return res, elapsed, nil
}

// execSandboxed executes the gear in isolation.
//
// The Spec says Writable: true for a call with no files, and that is a
// statement of what has always happened rather than a change: the payload has
// given the sandbox user ownership of everything since it replaced `docker cp`,
// so a gear has always been able to write beside its own code. Naming it here
// stops the flag being a lie. Tightening it for every gear would break any that
// writes a scratch file next to itself, so — like Spec.Network — it stays as it
// is until an operator decides otherwise.
//
// A call that carries files opts into the stricter shape: the code and in/ are
// root's and read-only, out/ is the sandbox user's, and out/ is what comes back.
func (e *Executor) execSandboxed(ctx context.Context, g Gear, dir, argsJSON string, timeout time.Duration, env map[string]string, ticket *gearnet.Ticket, fc *fileCall, onOutput func(stream, chunk string)) (Result, time.Duration, error) {
	bin, args := command(g)
	spec := sandbox.Spec{
		Dir:     dir,
		Command: bin,
		Args:    args,
		Stdin:   strings.NewReader(argsJSON),
		Env: withEnv(fixedEnv(map[string]string{"COGITORIUM_GEAR": g.Name, "HOME": "/tmp"},
			ticket, sandbox.HostAlias), env),
		TimeoutSeconds: int(timeout.Seconds()),
		// Declared for years and never set by anybody, which meant --network
		// none on every run. This is the caller the flag was waiting for: a
		// gear reaches the network when, and only when, the operator granted it
		// while reading the gear's source.
		Network:  g.NetworkGranted,
		OnOutput: onOutput,
		Writable: true,
		// The environment the operator granted, resolved to one of this
		// install's images. A gear never names an image.
		Image: e.imageFor(g),
		// Whether this run may be given a machine that has already run
		// something. A gear holding named values or the network is not: a warm
		// container shares /tmp with whatever ran in it before, and those are
		// exactly the runs that could leave a credential there. The sandbox
		// cannot work this out — only the caller knows what the run was given.
		Reusable: len(g.EnvNames) == 0 && !g.NetworkGranted,
	}
	if fc != nil {
		spec.Writable = false
		spec.Out = outDir
	}

	start := time.Now()
	out, runErr := e.sandbox.Run(ctx, spec)
	elapsed := time.Since(start)

	res := Result{
		Stdout:   truncate(out.Stdout),
		Stderr:   truncate(out.Stderr),
		ExitCode: out.ExitCode,
		TimedOut: out.TimedOut,
	}
	if runErr != nil {
		return res, elapsed, fmt.Errorf("gear %q: %w", g.Name, runErr)
	}
	return res, elapsed, nil
}

// materialize writes the approved version's files into dir. The directory is
// rebuilt from the database on every run: a deleted-and-reforged gear reuses
// name and version numbers, and stale files from the previous life must never
// execute under the new gear's approval.
//
// The caller chooses dir because a call carrying files needs a run of its own;
// see prepare.
func (e *Executor) materialize(ctx context.Context, g Gear, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear gear dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create gear dir: %w", err)
	}

	files, err := e.store.Files(ctx, g.ID, g.Version)
	if err != nil {
		return err
	}
	for _, f := range files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		// Defence in depth: paths are validated on forge, checked again here.
		if !strings.HasPrefix(target, dir+string(os.PathSeparator)) && target != dir {
			return fmt.Errorf("gear file %q escapes the gear directory", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create gear subdir for %q: %w", f.Path, err)
		}
		content := []byte(f.Content)
		if f.IsBinary() {
			decoded, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				return fmt.Errorf("gear file %q is not valid base64: %w", f.Path, err)
			}
			content = decoded
		}
		// 0644, not 0600: the enclosing directory is 0700, which is what
		// keeps other host users out. Inside the sandbox the process runs
		// as an unprivileged user that owns nothing, and it still has to
		// read its own code. The entrypoint of a binary gear also has to
		// be executable.
		mode := os.FileMode(0o644)
		if g.Runtime == RuntimeBinary && f.Path == g.Entrypoint {
			mode = 0o755
		}
		if err := os.WriteFile(target, content, mode); err != nil {
			return fmt.Errorf("write gear file %q: %w", f.Path, err)
		}
	}
	return nil
}

// envList flattens the environment for exec, sorted so that two identical runs
// produce identical argv-adjacent state and a diff of two runs is readable.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func truncate(s string) string {
	if len(s) <= maxOutputSize {
		return s
	}
	return s[:maxOutputSize] + "\n…[output truncated]"
}

// tap returns a writer that fills buf and, when on is set, reports each chunk
// as it arrives. Deliberately the same shape as the sandbox's own tap: one
// idea, so the two paths cannot disagree about what "live output" means.
func tap(buf *bytes.Buffer, stream string, on func(string, string)) io.Writer {
	if on == nil {
		return buf
	}
	return io.MultiWriter(buf, writerFunc(func(p []byte) (int, error) {
		on(stream, string(p))
		return len(p), nil
	}))
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
