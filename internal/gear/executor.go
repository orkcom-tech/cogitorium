package gear

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	execTimeout   = 60 * time.Second
	maxOutputSize = 64 * 1024
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

// Run executes a gear with argsJSON delivered on stdin. It refuses anything
// the operator has not approved.
func (e *Executor) Run(ctx context.Context, g Gear, argsJSON string) (Result, error) {
	if g.Status != StatusApproved {
		return Result{}, fmt.Errorf("gear %q (status %s): %w — the operator must approve it in the gear catalog first", g.Name, g.Status, ErrNotApproved)
	}

	dir, err := e.materialize(ctx, g)
	if err != nil {
		return Result{}, err
	}

	bin, preArgs := interpreter(g.Runtime)
	if _, err := exec.LookPath(bin); err != nil {
		return Result{}, fmt.Errorf("gear %q needs %s, which is not installed on this machine", g.Name, bin)
	}

	runCtx, cancel := context.WithTimeout(ctx, execTimeout)
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
	res := Result{
		Stdout:   truncate(stdout.String()),
		Stderr:   truncate(stderr.String()),
		ExitCode: cmd.ProcessState.ExitCode(),
		TimedOut: runCtx.Err() == context.DeadlineExceeded,
	}
	slog.Info("gear executed", "gear", g.Name, "version", g.Version, "exit_code", res.ExitCode,
		"timed_out", res.TimedOut, "duration_ms", time.Since(start).Milliseconds())

	if res.TimedOut {
		return res, fmt.Errorf("gear %q timed out after %s", g.Name, execTimeout)
	}
	if runErr != nil && res.ExitCode == 0 {
		// Failed to start at all (not a non-zero exit).
		return res, fmt.Errorf("gear %q could not run: %w", g.Name, runErr)
	}
	return res, nil
}

// materialize writes the approved version's files into
// <data-dir>/gears/<name>/v<version>/ if they are not already there.
func (e *Executor) materialize(ctx context.Context, g Gear) (string, error) {
	dir := filepath.Join(e.baseDir, g.Name, fmt.Sprintf("v%d", g.Version))
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
