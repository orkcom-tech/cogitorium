package gear

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/sandbox"
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
}

func NewExecutor(store *Store, dataDir string, sb sandbox.Runner) *Executor {
	if sb == nil {
		slog.Warn("gears will run as unsandboxed subprocesses with this server's file access — " +
			"an approved gear can read the database, including provider API keys; " +
			"install Docker or set sandbox: docker to isolate them")
	}
	return &Executor{store: store, baseDir: filepath.Join(dataDir, "gears"), dataDir: dataDir, sandbox: sb}
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

	dir, fc, cleanup, err := e.prepare(ctx, g, files, caller)
	if err != nil {
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

	var (
		res     Result
		elapsed time.Duration
		runErr  error
	)
	// From here a process was started, or was attempted with everything needed
	// to start it present — so everything below records a run that happened.
	if e.sandbox != nil {
		res, elapsed, runErr = e.execSandboxed(ctx, g, dir, argsJSON, timeout, fc, onOutput)
	} else {
		res, elapsed, runErr = e.execSubprocess(ctx, g, dir, argsJSON, timeout, onOutput)
	}

	// Collected whatever happened: a gear that wrote its result and then exited
	// non-zero still wrote its result.
	if fc != nil {
		produced, ignored, cerr := collectOut(dir, fc.root, fc.destRel)
		res.Produced, res.Ignored = produced, ignored
		if cerr != nil {
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
		"duration_ms", elapsed.Milliseconds())

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

// fileCall is what a call that carries files needs to remember for afterwards:
// whose workspace it belongs to, and where in it the gear's output goes.
type fileCall struct {
	root    string // the workspace's directory on this machine
	destRel string // where out/ lands, relative to the workspace root
}

// prepare builds the directory the gear runs in, and returns the cleanup that
// must follow it.
//
// A call that names no files gets the directory it has always got:
// <data>/gears/<name>/v<version>, rebuilt from the database and left in place
// afterwards. A call that carries files gets a run of its own — because two
// agents in two workspaces can call the same gear at the same moment, and one
// caller's in/ appearing in another caller's run would be a file crossing
// between workspaces. It is removed afterwards for the same reason: a
// workspace's files have no business staying in the gear directory once the run
// that borrowed them is over.
func (e *Executor) prepare(ctx context.Context, g Gear, files []string, caller Caller) (string, *fileCall, func(), error) {
	noop := func() {}
	base := filepath.Join(e.baseDir, g.Name, fmt.Sprintf("v%d", g.Version))
	if files == nil {
		if err := e.materialize(ctx, g, base); err != nil {
			return "", nil, noop, err
		}
		return base, nil, noop, nil
	}

	if caller.WorkspaceID == nil {
		return "", nil, noop, errors.New("a gear can only be given files on behalf of a workspace, and this run has none — " +
			"an operator's dry run from the catalog has no workspace to take files from or hand them back to")
	}
	root := workdir.Dir(e.dataDir, *caller.WorkspaceID)
	if root == "" {
		return "", nil, noop, fmt.Errorf("workspace %d has no working directory on this machine, so it has no files to give", *caller.WorkspaceID)
	}
	token, err := runToken()
	if err != nil {
		return "", nil, noop, err
	}

	dir := base + ".run-" + token
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("could not remove a gear run directory", "gear", g.Name, "dir", dir, "err", err)
		}
	}
	if err := e.materialize(ctx, g, dir); err != nil {
		cleanup()
		return "", nil, noop, err
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
func (e *Executor) execSubprocess(ctx context.Context, g Gear, dir, argsJSON string, timeout time.Duration, onOutput func(stream, chunk string)) (Result, time.Duration, error) {
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
	// the server's environment (which may hold provider API keys).
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"COGITORIUM_GEAR=" + g.Name,
	}

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
func (e *Executor) execSandboxed(ctx context.Context, g Gear, dir, argsJSON string, timeout time.Duration, fc *fileCall, onOutput func(stream, chunk string)) (Result, time.Duration, error) {
	bin, args := command(g)
	spec := sandbox.Spec{
		Dir:            dir,
		Command:        bin,
		Args:           args,
		Stdin:          strings.NewReader(argsJSON),
		Env:            map[string]string{"COGITORIUM_GEAR": g.Name, "HOME": "/tmp"},
		TimeoutSeconds: int(timeout.Seconds()),
		OnOutput:       onOutput,
		Writable:       true,
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
