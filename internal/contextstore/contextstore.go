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

// EnsureSpace creates the context space when contextd is installed and has
// none.
//
// WHY THE SERVER DOES THIS AND NOT AN INSTALLER. A binary is not a working
// space: with contextd present but uninitialised, every context surface in the
// product answers "no context space initialized — run: contextd init solo", and
// memory does nothing. The container image has run that exact command in its
// entrypoint from the start; every other way of installing — Homebrew, Scoop, a
// package, the archive, the desktop app — left the person to find that sentence
// and act on it. Doing it here covers all of them at once, in the place that
// already knows whether contextd is reachable.
//
// IT REACHES THE NETWORK, once. `contextd init solo` fetches its space template
// from a public repository and caches it, so a machine with no outbound route
// gets a space that was not created rather than one half-created. That is worth
// saying in the log rather than burying, which is why the attempt is logged as
// well as the outcome.
//
// Never fatal, and idempotent: an existing space is left exactly as it is.
func (s *Store) EnsureSpace(ctx context.Context) {
	st := s.CheckStatus(ctx)
	switch {
	case st.Available:
		return
	case st.Version == "":
		// No contextd at all. Its absence is already reported by every surface
		// that asks, and inventing a space is not this function's business.
		return
	case !strings.Contains(st.Error, "no context space initialized"):
		// Present and broken for some other reason. Initialising on top of
		// that would turn a legible failure into a confusing one.
		slog.Warn("contextd is installed but not answering; leaving the space alone", "err", st.Error)
		return
	}

	slog.Info("no context space yet — creating one (contextd fetches its template over the network)",
		"space_root", st.SpaceRoot)
	if _, err := s.run(ctx, nil, "init", "solo", "--name", "cogitorium", "--role", "workbench"); err != nil {
		slog.Warn("could not create the context space; memory reports unavailable until one exists",
			"err", err, "fix", "contextd init solo --name cogitorium --role workbench")
		return
	}
	slog.Info("context space ready", "space_root", s.CheckStatus(ctx).SpaceRoot)
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
// read, and it is a real compare-and-swap.
//
// WHAT THIS FIXES. Two people open the same context document, the first saves,
// the second saves, and the first person's work is gone with no message
// anywhere. Nothing about the interface suggested that could happen — the
// editor showed a file and a save button.
//
// HOW IT CLOSES. contextd takes the expected version now:
// `contextd file put <path> --from - --if-version v4` is refused with a
// storage conflict if the file has moved past v4. The check and the write are
// ONE call inside contextd, against its own storage, so there is no window
// between them for a third party to write into.
//
// This used to be a read-to-write guard — read the version, compare it here,
// then write — because the CLI took no such argument, and that left a race
// this code could not close from outside. Rather than document the hole
// forever, the flag was added upstream: contextd v1.0.0. See its CHANGELOG.
//
// An older contextd rejects the flag rather than ignoring it, so a Cogitorium
// running against one fails the save loudly instead of silently going back to
// last-write-wins. That is the right way round: a guard that quietly stops
// guarding is worse than one that says it cannot.
func (s *Store) PutIfUnchanged(ctx context.Context, path, content, expected string) error {
	if err := validPath(path); err != nil {
		return err
	}
	args := []string{"file", "put", path, "--from", "-"}
	// An empty expectation means the caller never read a version — a new file,
	// or a client that predates this. Refusing those would break writing a
	// file that does not exist yet, which is most of what Put is for.
	if expected != "" {
		args = append(args, "--if-version", expected)
	}
	_, err := s.run(ctx, []byte(content), args...)
	if err != nil {
		if isConflict(err) {
			return fmt.Errorf("%w: somebody changed it since you opened %s — reopen it and reapply your change",
				ErrStale, expected)
		}
		return err
	}
	slog.Info("context file written via contextd", "path", path, "bytes", len(content), "if_version", expected)
	return nil
}

// isConflict reports whether contextd refused a write because the file moved.
//
// By the message rather than the exit code: contextd maps a CAS conflict to
// exit 3 on `put` — which run() already turns into ErrConflict — but a refusal
// that arrives through a different path would otherwise read as an ordinary
// failure. Both are checked, so neither spelling gets past.
func isConflict(err error) bool {
	if errors.Is(err, ErrConflict) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "conflict")
}

// Delete removes a document from the space, keeping its history.
//
// WHAT THIS REPLACED. Forgetting was clearing: an emptied document is skipped
// when a prompt is assembled, so the agent genuinely stopped being told it, and
// that was the whole of what could be offered — `contextd` had no delete at
// all. Its storage layer had one and no command reached it, which is a thing
// this product could see and could not use.
//
// It is a soft delete, which is contextd's own design and the right one: the
// live copy goes, every version stays, and `contextd file undelete` brings it
// back. So "forget this" here is reversible in the space, and that is worth
// saying out loud in the interface rather than implying an erasure that did not
// happen.
//
// expected is the version the caller read, and it is passed through for the
// same reason it is on a write: removing a document somebody has just rewritten
// is exactly as destructive as overwriting it.
func (s *Store) Delete(ctx context.Context, path, expected string) error {
	if err := validPath(path); err != nil {
		return err
	}
	args := []string{"file", "delete", path}
	if expected != "" {
		args = append(args, "--if-version", expected)
	}
	_, err := s.run(ctx, nil, args...)
	if err != nil {
		if isConflict(err) {
			return fmt.Errorf("%w: somebody changed it since you opened %s — reopen it and decide again",
				ErrStale, expected)
		}
		return err
	}
	slog.Info("context file deleted via contextd", "path", path, "if_version", expected)
	return nil
}
