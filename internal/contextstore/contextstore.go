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
	"strconv"
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

// Match is one line in the space that contains what was searched for.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SearchResult is what a search found, and whether it stopped early.
type SearchResult struct {
	Query string  `json:"query"`
	Files int     `json:"files_matched"`
	Total int     `json:"files_scanned"`
	Cut   bool    `json:"truncated"`
	Hits  []Match `json:"matches"`
}

// Search looks inside the space's files, not only at their names.
//
// WHAT THIS FIXES. The only way to find a memory was to know its path. An
// operator could list the space and read a file; they could not ask "which of
// these two hundred documents mentions the retry policy". Nor could an agent —
// the read_memory tool takes a path, so an agent that has not been handed the
// right path cannot reach a fact that is sitting in the space it is entitled
// to read. In a product whose whole claim is that agents share a durable
// memory, "you must already know where it is" is most of that claim missing.
//
// contextd's own `search` does the looking, for the same reason the rest of
// this package shells out rather than reading files: the space's layout,
// versioning and access rules belong to contextd, and a second implementation
// that walked the directory would be a second set of rules that drift.
//
// The limit is passed through rather than applied afterwards so that contextd
// can stop early, and Cut says whether it did — a truncated answer that does
// not say it is truncated reads as "there is nothing else", which is the one
// wrong answer a search can give.
func (s *Store) Search(ctx context.Context, query, pathGlob string, limit int) (SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, errors.New("a search needs something to look for")
	}
	// A query starting with a dash would be read as a flag by any CLI. The
	// same guard validPath applies to paths, for the same reason.
	if strings.HasPrefix(query, "-") {
		return SearchResult{}, fmt.Errorf("a search cannot start with %q — it would be read as an option", "-")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []string{"search", query, "--json", "--limit", strconv.Itoa(limit)}
	if pathGlob != "" {
		if strings.HasPrefix(pathGlob, "-") {
			return SearchResult{}, errors.New("a path filter cannot start with a dash")
		}
		args = append(args, "--path", pathGlob)
	}
	out, err := s.run(ctx, nil, args...)
	if err != nil {
		return SearchResult{}, err
	}
	var res SearchResult
	if err := json.Unmarshal(out, &res); err != nil {
		return SearchResult{}, fmt.Errorf("parse contextd search: %w", err)
	}
	if res.Hits == nil {
		res.Hits = []Match{}
	}
	return res, nil
}

// Version returns the version contextd currently holds for a path, and whether
// it holds one at all.
//
// It is what makes the save guard possible. See PutIfUnchanged.
func (s *Store) Version(ctx context.Context, path string) (string, bool, error) {
	files, err := s.List(ctx)
	if err != nil {
		return "", false, err
	}
	for _, f := range files {
		if f.Path == path {
			return f.Version, true, nil
		}
	}
	return "", false, nil
}

// ErrStale is a save refused because the file changed since it was read.
var ErrStale = errors.New("this file changed since you opened it")

// PutIfUnchanged writes only if the file is still at the version the editor
// read.
//
// WHAT THIS FIXES. Two people open the same context document, the first saves,
// the second saves, and the first person's work is gone with no message
// anywhere. Nothing about the interface suggested that could happen — the
// editor showed a file and a save button.
//
// AND WHAT IT DOES NOT FIX, said plainly rather than left to be discovered:
// this is a read-to-write guard, not a compare-and-swap. contextd's CLI takes
// no expected-version argument (`contextd file put --help` offers only
// --from), so the check and the write are two calls, and a third party writing
// in the microseconds between them still wins. Closing that needs an upstream
// flag — `--if-version` or equivalent — and until it exists this is what is
// honestly available: it turns the COMMON case, where the other edit happened
// minutes or hours ago, from silent loss into a refusal the operator can act
// on. The race it cannot close is stated in the refusal's own wording nowhere,
// because a person who hit a one-microsecond race does not need a lecture; it
// is stated here, for whoever maintains this.
func (s *Store) PutIfUnchanged(ctx context.Context, path, content, expected string) error {
	if err := validPath(path); err != nil {
		return err
	}
	// An empty expectation means the caller never read a version — a new file,
	// or a client that predates this. Refusing those would break writing a
	// file that does not exist yet, which is most of what Put is for.
	if expected != "" {
		current, exists, err := s.Version(ctx, path)
		if err != nil {
			return err
		}
		if exists && current != expected {
			return fmt.Errorf("%w: it is at %s and you opened %s — reopen it and reapply your change",
				ErrStale, current, expected)
		}
	}
	return s.Put(ctx, path, content)
}
