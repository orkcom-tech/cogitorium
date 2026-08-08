// Package contextstore is Cogitorium's seam to Contextverse: context
// content and versioning belong to contextd, Cogitorium only reads, writes
// through it, and remembers which paths feed which agents. The MVP backend
// drives the contextd CLI (Contextverse's declared primary surface, typed
// exit codes, --json); a contextd server REST backend is post-MVP.
package contextstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

var (
	// ErrConflict maps contextd exit code 3 (CAS conflict on put).
	ErrConflict = errors.New("version conflict — the file changed since it was read")
	// ErrUnavailable means contextd itself can't be run or has no space.
	ErrUnavailable = errors.New("contextd is not available")
	// ErrNoSuchPath means the space has no such file — a bad request, not a
	// server fault.
	ErrNoSuchPath = errors.New("no such path in the context space")
)

type File struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type Status struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	SpaceRoot string `json:"space_root,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Store shells out to one contextd binary operating on its default space.
type Store struct {
	bin string
}

func New(bin string) *Store {
	if bin == "" {
		bin = "contextd"
	}
	return &Store{bin: bin}
}

func validPath(path string) error {
	if path == "" {
		return errors.New("path is required")
	}
	if strings.HasPrefix(path, "-") || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return fmt.Errorf("invalid context path %q", path)
	}
	return nil
}

// run executes contextd with args, returning stdout. Typed contextd exit
// codes are mapped: 3 = CAS conflict, anything else non-zero = error with
// stderr attached.
func (s *Store) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		raw := strings.TrimSpace(stderr.String())
		slog.Warn("contextd command failed", "args", args, "exit_code", exitErr.ExitCode(), "stderr", raw)
		if exitErr.ExitCode() == 3 {
			return nil, fmt.Errorf("contextd %s: %w", args[0], ErrConflict)
		}
		why := reason(raw)
		if strings.Contains(why, "not found") {
			return nil, fmt.Errorf("contextd %s: %w", strings.Join(args, " "), ErrNoSuchPath)
		}
		return nil, fmt.Errorf("contextd %s failed: %s", strings.Join(args, " "), why)
	}
	slog.Warn("contextd could not run", "bin", s.bin, "err", err)
	return nil, fmt.Errorf("%w: %s (configure contextd_path in config.yaml or put contextd on PATH)", ErrUnavailable, err)
}

// reason extracts the human-readable cause from contextd's stderr, which
// interleaves structured log lines with a final "error: …" summary. The
// full text still goes to the log; callers and the UI get the summary.
func reason(stderr string) string {
	lines := strings.Split(stderr, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if after, ok := strings.CutPrefix(line, "error: "); ok {
			return after
		}
	}
	if stderr == "" {
		return "no error output"
	}
	return lines[len(lines)-1]
}

// CheckStatus reports whether contextd runs, which version it is, and
// whether it has an initialized space — the basis for install-time and
// runtime "is Contextverse here" checks (requirement 16).
func (s *Store) CheckStatus(ctx context.Context) Status {
	version := ""
	if raw, err := s.run(ctx, nil, "version"); err == nil {
		version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "contextd"))
	}

	out, err := s.run(ctx, nil, "status", "--json")
	if err != nil {
		return Status{Available: false, Version: version, Error: err.Error()}
	}
	var st struct {
		SpaceRoot string `json:"space_root"`
		Exists    bool   `json:"exists"`
		Mode      string `json:"mode"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return Status{Available: false, Version: version, Error: "unparseable contextd status: " + err.Error()}
	}
	if !st.Exists {
		return Status{Available: false, Version: version, SpaceRoot: st.SpaceRoot, Mode: st.Mode,
			Error: "no context space initialized — run: contextd init solo"}
	}
	return Status{Available: true, Version: version, SpaceRoot: st.SpaceRoot, Mode: st.Mode}
}

func (s *Store) List(ctx context.Context) ([]File, error) {
	out, err := s.run(ctx, nil, "file", "list", "--json")
	if err != nil {
		return nil, err
	}
	var files []File
	if err := json.Unmarshal(out, &files); err != nil {
		return nil, fmt.Errorf("parse contextd file list: %w", err)
	}
	return files, nil
}

func (s *Store) Get(ctx context.Context, path string) (string, error) {
	if err := validPath(path); err != nil {
		return "", err
	}
	out, err := s.run(ctx, nil, "file", "get", path)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Put writes through contextd. Known limitation: the contextd CLI's put
// takes no expected-version argument, so the CAS protection is contextd's
// own write-time check, not a read-to-write guard — two operators editing
// the same file can still last-write-win. Closing that needs an upstream
// contextd flag (e.g. --if-version); tracked, not worked around here.
func (s *Store) Put(ctx context.Context, path, content string) error {
	if err := validPath(path); err != nil {
		return err
	}
	_, err := s.run(ctx, []byte(content), "file", "put", path, "--from", "-")
	if err == nil {
		slog.Info("context file written via contextd", "path", path, "bytes", len(content))
	}
	return err
}
