package gear

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
}

func NewExecutor(store *Store, dataDir string) *Executor {
	return &Executor{store: store, baseDir: filepath.Join(dataDir, "gears")}
}

type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
}

func interpreter(runtime string) (string, []string) {
	switch runtime {
	case "python":
		return "python3", nil
	case "node":
		return "node", nil
	default:
		return "bash", nil
	}
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
	if g.Status != StatusApproved && !caller.DryRun {
		return Result{}, fmt.Errorf("gear %q (status %s): %w — the operator must approve it in the gear catalog first", g.Name, g.Status, ErrNotApproved)
	}
	if caller.DryRun && caller.AgentID != nil {
		// Defence in depth: a dry run is an operator act by definition.
		return Result{}, errors.New("a dry run cannot be performed on behalf of an agent")
	}

	dir, err := e.materialize(ctx, g)
	if err != nil {
		return Result{}, err
	}

	bin, preArgs := interpreter(g.Runtime)
	if _, err := exec.LookPath(bin); err != nil {
		return Result{}, fmt.Errorf("gear %q needs %s, which is not installed on this machine", g.Name, bin)
	}

	timeout := defaultTimeout
	if g.TimeoutSeconds > 0 {
		timeout = time.Duration(g.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append(append([]string{}, preArgs...), g.Entrypoint)
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dir
	if argsJSON == "" {
		argsJSON = "{}"
	}
	cmd.Stdin = strings.NewReader(argsJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Keep the environment minimal: a forged tool has no business reading
	// the server's environment (which may hold provider API keys).
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"COGITORIUM_GEAR=" + g.Name,
	}

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)
	res := Result{
		Stdout:   truncate(stdout.String()),
		Stderr:   truncate(stderr.String()),
		ExitCode: cmd.ProcessState.ExitCode(),
		TimedOut: runCtx.Err() == context.DeadlineExceeded,
	}
	slog.Info("gear executed", "gear", g.Name, "version", g.Version, "exit_code", res.ExitCode,
		"timed_out", res.TimedOut, "dry_run", caller.DryRun, "duration_ms", elapsed.Milliseconds())

	// Recorded even when the caller's context is done: an execution that
	// happened must appear in the operator's audit trail.
	if err := e.store.RecordRun(context.WithoutCancel(ctx), Run{
		GearID: g.ID, Version: g.Version, AgentID: caller.AgentID, WorkspaceID: caller.WorkspaceID,
		Args: argsJSON, ExitCode: res.ExitCode, TimedOut: res.TimedOut,
		DurationMs: elapsed.Milliseconds(), Stdout: res.Stdout, Stderr: res.Stderr,
	}); err != nil {
		slog.Error("could not record gear run", "gear", g.Name, "err", err)
	}

	if res.TimedOut {
		return res, fmt.Errorf("gear %q timed out after %s", g.Name, timeout)
	}
	if runErr != nil && res.ExitCode == 0 {
		// Failed to start at all (not a non-zero exit).
		return res, fmt.Errorf("gear %q could not run: %w", g.Name, runErr)
	}
	return res, nil
}

// materialize writes the approved version's files into
// <data-dir>/gears/<name>/v<version>/. The directory is rebuilt from the
// database on every run: a deleted-and-reforged gear reuses name and
// version numbers, and stale files from the previous life must never
// execute under the new gear's approval.
func (e *Executor) materialize(ctx context.Context, g Gear) (string, error) {
	dir := filepath.Join(e.baseDir, g.Name, fmt.Sprintf("v%d", g.Version))
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear gear dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create gear dir: %w", err)
	}

	files, err := e.store.Files(ctx, g.ID, g.Version)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		// Defence in depth: paths are validated on forge, checked again here.
		if !strings.HasPrefix(target, dir+string(os.PathSeparator)) && target != dir {
			return "", fmt.Errorf("gear file %q escapes the gear directory", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", fmt.Errorf("create gear subdir for %q: %w", f.Path, err)
		}
		if err := os.WriteFile(target, []byte(f.Content), 0o600); err != nil {
			return "", fmt.Errorf("write gear file %q: %w", f.Path, err)
		}
	}
	return dir, nil
}

func truncate(s string) string {
	if len(s) <= maxOutputSize {
		return s
	}
	return s[:maxOutputSize] + "\n…[output truncated]"
}
