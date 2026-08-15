//go:build !windows

package contextstore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Everything here drives a real contextd stand-in: a /bin/sh program written
// into t.TempDir() that records how it was called and then answers with a
// chosen stdout, stderr and exit status. That is a real process with a real
// wait status — the thing this package's whole contract is built on — not a
// mock of the package under test. Nothing here needs a network, a container,
// or a Contextverse install.
//
// The tag is because the stand-in is a shell script. Tests run on Linux in CI;
// Windows is cross-compiled only.

// reply is what the stand-in says for one contextd subcommand.
type reply struct {
	stdout string
	stderr string
	code   int
}

type fakeContextd struct {
	path      string // hand this to New
	callsFile string
	stdinFile string
}

// newFakeContextd writes the stand-in. Replies are keyed by contextd's first
// argument (the subcommand), because CheckStatus runs two different ones in a
// single call and each has to answer differently; "default" catches the rest.
func newFakeContextd(t *testing.T, replies map[string]reply) *fakeContextd {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh to build the contextd stand-in: %v", err)
	}
	// Resolved once, absolutely: one test empties PATH on purpose, and the
	// stand-in has to keep working when it does.
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Skipf("no cat to build the contextd stand-in: %v", err)
	}
	dir := t.TempDir()
	f := &fakeContextd{
		path:      filepath.Join(dir, "contextd"),
		callsFile: filepath.Join(dir, "calls"),
		stdinFile: filepath.Join(dir, "stdin"),
	}
	all := map[string]reply{"default": {}}
	for k, v := range replies {
		all[k] = v
	}
	for key, r := range all {
		writeFile(t, filepath.Join(dir, "out."+key), r.stdout)
		writeFile(t, filepath.Join(dir, "err."+key), r.stderr)
		writeFile(t, filepath.Join(dir, "code."+key), strconv.Itoa(r.code))
	}

	// Args are joined by the ASCII unit separator so an argument containing a
	// space (or an empty one, which is exactly what a dropped path check would
	// produce) stays distinguishable in the recording.
	script := `#!/bin/sh
d=` + shellQuote(dir) + `
cat=` + shellQuote(catPath) + `
sep=$(printf '\037')
line=""
first=1
for a in "$@"; do
	if [ "$first" -eq 1 ]; then line="$a"; first=0; else line="$line$sep$a"; fi
done
printf '%s\n' "$line" >> "$d/calls"
"$cat" >> "$d/stdin"
key="$1"
if [ ! -f "$d/code.$key" ]; then key=default; fi
[ -f "$d/out.$key" ] && "$cat" "$d/out.$key"
[ -f "$d/err.$key" ] && "$cat" "$d/err.$key" >&2
exit "$("$cat" "$d/code.$key")"
`
	if err := os.WriteFile(f.path, []byte(script), 0o755); err != nil {
		t.Fatalf("write contextd stand-in: %v", err)
	}
	waitExecutable(t, f)
	return f
}

// waitExecutable runs the stand-in once, and keeps trying while Linux answers
// ETXTBSY.
//
// os.WriteFile closes the file before returning, so the write itself is
// finished — but these tests run in parallel and each builds its own stand-in,
// and a fork from any other goroutine between this file's open and close
// inherits the writable descriptor. Linux then refuses to exec it until that
// descriptor is gone. It is a race in the FIXTURE, not in what is being
// tested, and it surfaced as CI reporting "text file busy" for a conflict test
// that had nothing to do with exec.
//
// Proving the binary runs before handing it to the code under test moves the
// flake here, where it can be waited out, instead of failing an assertion
// about something else entirely. The warm-up call is then erased: it would
// otherwise appear in the recorded calls that several tests count.
func waitExecutable(t *testing.T, f *fakeContextd) {
	t.Helper()
	for range 100 {
		cmd := exec.Command(f.path, "--warmup")
		cmd.Stdin = strings.NewReader("")
		err := cmd.Run()
		if errors.Is(err, syscall.ETXTBSY) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		// Any other outcome means it executed — the stand-in exits non-zero on
		// purpose in several of these tests, and that is not a problem here.
		for _, leftover := range []string{f.callsFile, f.stdinFile} {
			if err := os.Remove(leftover); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("clear the warm-up trace: %v", err)
			}
		}
		return
	}
	t.Fatal("the contextd stand-in was still \"text file busy\" a second after being written")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// calls returns one entry per invocation, each the exact argv contextd was
// given. No invocations at all means the file was never created.
func (f *fakeContextd) calls(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(f.callsFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read recorded calls: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		out = append(out, strings.Split(line, "\x1f"))
	}
	return out
}

func (f *fakeContextd) stdin(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.stdinFile)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	return string(raw)
}

// TestArgvSentToContextd pins the literal command line for every operation.
// These are contextd's public CLI surface: get the words or the order wrong
// and every context read and write in the product fails against a real
// contextd, while any test that only checked "we ran something" stays green.
func TestArgvSentToContextd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name    string
		replies map[string]reply
		call    func(t *testing.T, s *Store)
		want    [][]string
	}{
		{
			name:    "list asks for JSON, because parsing contextd's human output would break on any wording change",
			replies: map[string]reply{"default": {stdout: `[]`}},
			call: func(t *testing.T, s *Store) {
				if _, err := s.List(ctx); err != nil {
					t.Fatalf("List: %v", err)
				}
			},
			want: [][]string{{"file", "list", "--json"}},
		},
		{
			name:    "get passes the path through untouched",
			replies: map[string]reply{"default": {stdout: "hello"}},
			call: func(t *testing.T, s *Store) {
				if _, err := s.Get(ctx, "notes/a.md"); err != nil {
					t.Fatalf("Get: %v", err)
				}
			},
			want: [][]string{{"file", "get", "notes/a.md"}},
		},
		{
			name:    "put reads the document from stdin, not from a file or an argument",
			replies: map[string]reply{},
			call: func(t *testing.T, s *Store) {
				if err := s.Put(ctx, "notes/a.md", "hello"); err != nil {
					t.Fatalf("Put: %v", err)
				}
			},
			want: [][]string{{"file", "put", "notes/a.md", "--from", "-"}},
		},
		{
			name: "status asks contextd its version first, then the space, in that order",
			replies: map[string]reply{
				"version": {stdout: "contextd 0.4.1\n"},
				"status":  {stdout: `{"space_root":"/s","exists":true,"mode":"solo"}`},
			},
			call: func(t *testing.T, s *Store) { s.CheckStatus(ctx) },
			want: [][]string{{"version"}, {"status", "--json"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeContextd(t, tc.replies)
			tc.call(t, New(f.path))
			if got := f.calls(t); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("contextd was called as %q, want %q", got, tc.want)
			}
		})
	}
}

// A document has to reach contextd byte for byte. Handing it over on the
// command line instead would mangle anything with a newline and blow past
// ARG_MAX on a large file; handing over nothing would silently save an empty
// document over the operator's work and report success.
func TestPutStreamsTheDocumentOnStdin(t *testing.T) {
	t.Parallel()
	f := newFakeContextd(t, nil)

	doc := "# Notes\n\n- \"quoted\" and 'quoted'\n--not-a-flag\nтекст\ttab\nno trailing newline"
	if err := New(f.path).Put(context.Background(), "notes/a.md", doc); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := f.stdin(t); got != doc {
		t.Errorf("contextd received %q on stdin, want the document verbatim %q", got, doc)
	}
}

// Get must not touch what contextd printed. Trimming would drop the trailing
// newline on every read, and the next Put would write the trimmed version
// back — a document that loses a byte each time it is opened and saved.
func TestGetReturnsContentVerbatim(t *testing.T) {
	t.Parallel()
	body := "  leading and trailing space  \n\nline two\n"
	f := newFakeContextd(t, map[string]reply{"default": {stdout: body}})

	got, err := New(f.path).Get(context.Background(), "notes/a.md")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != body {
		t.Errorf("Get returned %q, want the bytes contextd printed %q", got, body)
	}
}

// The wire contract with contextd's `file list --json`. If these field names
// drift, the context browser lists a space full of blank rows.
func TestListParsesContextdJSON(t *testing.T) {
	t.Parallel()
	f := newFakeContextd(t, map[string]reply{"default": {
		stdout: `[{"path":"shared/brief.md","version":"3"},{"path":"agents/scout/memory.md","version":"11"}]`,
	}})

	got, err := New(f.path).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []File{
		{Path: "shared/brief.md", Version: "3"},
		{Path: "agents/scout/memory.md", Version: "11"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}

// A contextd that answers with something unparseable is broken, and the
// operator has to be told. Swallowing the parse error would show an empty
// context space — indistinguishable from a space that really is empty, which
// is how an agent quietly loses all its memory with nothing in the UI to say
// so.
func TestListRejectsUnparseableOutput(t *testing.T) {
	t.Parallel()
	f := newFakeContextd(t, map[string]reply{"default": {stdout: "shared/brief.md  v3\n"}})

	got, err := New(f.path).List(context.Background())
	if err == nil {
		t.Fatalf("List accepted non-JSON output and returned %v", got)
	}
	if got != nil {
		t.Errorf("List returned %v alongside an error; callers must get nothing", got)
	}
}

// Exit code 3 is contextd's typed CAS conflict. The server turns it into a
// 409 so the UI can say "someone else edited this, reload"; anything else
// becomes a 500 and the operator is told the server broke instead.
func TestExitCodeThreeIsAConflict(t *testing.T) {
	t.Parallel()
	f := newFakeContextd(t, map[string]reply{"default": {
		code:   3,
		stderr: "error: file changed since it was read\n",
	}})

	err := New(f.path).Put(context.Background(), "notes/a.md", "new body")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("exit 3 gave %v, want ErrConflict", err)
	}
	// The other typed answers must not also claim it — each maps to a
	// different HTTP status.
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoSuchPath) {
		t.Errorf("a conflict must not also be unavailable or not-found: %v", err)
	}
}

// Exit codes that are not 3 are ordinary failures, and the message the
// operator sees has to be contextd's own summary line. contextd interleaves
// structured log lines with its "error: …" summary and can print more log
// lines after it, so neither "the whole stderr" nor "the last line" is the
// answer. The full text still goes to the log.
func TestNonZeroExitReportsTheErrorSummaryNotTheLog(t *testing.T) {
	t.Parallel()
	const noise = `time=2026-01-02T03:04:05Z level=INFO msg="opening space" root=/home/u/.contextd
time=2026-01-02T03:04:05Z level=WARN msg="lock held" pid=4242
error: space is locked by another process
time=2026-01-02T03:04:06Z level=INFO msg="shutting down" code=1
`
	f := newFakeContextd(t, map[string]reply{"default": {code: 1, stderr: noise}})

	_, err := New(f.path).Get(context.Background(), "notes/a.md")
	if err == nil {
		t.Fatal("a non-zero exit must be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "space is locked by another process") {
		t.Errorf("error %q does not carry contextd's own summary", msg)
	}
	if strings.Contains(msg, "level=INFO") || strings.Contains(msg, "shutting down") {
		t.Errorf("error %q dumps contextd's log at the operator instead of the summary", msg)
	}
	// Which command failed, so the message is actionable on its own.
	if !strings.Contains(msg, "file get") {
		t.Errorf("error %q does not say which contextd command failed", msg)
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoSuchPath) {
		t.Errorf("a plain failure must not be typed as one of the mapped errors: %v", err)
	}
}

// Asking for a path the space does not have is a bad request, not a server
// fault: the server answers 404. Without this the "bind this path to an
// agent" flow reports a 500 when the operator simply mistyped a filename.
func TestMissingPathIsNotAServerFault(t *testing.T) {
	t.Parallel()
	f := newFakeContextd(t, map[string]reply{"default": {
		code:   1,
		stderr: "error: file not found: notes/missing.md\n",
	}})

	_, err := New(f.path).Get(context.Background(), "notes/missing.md")
	if !errors.Is(err, ErrNoSuchPath) {
		t.Fatalf("a missing file gave %v, want ErrNoSuchPath", err)
	}
}

// contextd not being installed is the single most likely first-run failure,
// and it is the one case the operator can fix. It must arrive as
// ErrUnavailable (503, "the dependency is missing") rather than a 500, and it
// must say where to point Cogitorium at it.
func TestUnrunnableBinaryIsUnavailableAndSaysHowToFixIt(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "contextd")
	s := New(missing)

	_, err := s.List(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a missing binary gave %v, want ErrUnavailable", err)
	}
	for _, want := range []string{"contextd_path", "config.yaml", "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q, so the operator is not told how to fix it", err, want)
		}
	}

	st := s.CheckStatus(context.Background())
	if st.Available {
		t.Error("CheckStatus reports available with no contextd binary at all")
	}
	if st.Error == "" {
		t.Error("CheckStatus reports unavailable with no reason; the install check has nothing to show")
	}
}

// A path arrives from a URL query parameter. contextd's own flag parser is on
// the other side of it, so anything starting with "-" is read as a flag and
// ".." walks out of the space — the difference between "read a context file"
// and "read /etc/passwd" or "run whatever flag the caller named". The
// rejection has to happen before the process starts, which is why every
// rejected case also asserts contextd was never invoked.
func TestRejectedPathsNeverReachContextd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		wantReject bool
	}{
		{name: "empty", path: "", wantReject: true},
		{name: "parent escape", path: "../secrets.md", wantReject: true},
		{name: "escape in the middle", path: "notes/../../etc/passwd", wantReject: true},
		{name: "absolute", path: "/etc/passwd", wantReject: true},
		{name: "long flag", path: "--help", wantReject: true},
		{name: "short flag", path: "-f", wantReject: true},
		// The positive cases matter as much: a validator that rejects
		// everything would satisfy the list above while making the product
		// unable to read a single file.
		{name: "plain file", path: "a.md", wantReject: false},
		{name: "nested file", path: "agents/scout/memory.md", wantReject: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, op := range []string{"get", "put"} {
				f := newFakeContextd(t, map[string]reply{"default": {stdout: "body"}})
				s := New(f.path)

				var err error
				if op == "get" {
					_, err = s.Get(context.Background(), tc.path)
				} else {
					err = s.Put(context.Background(), tc.path, "body")
				}

				calls := f.calls(t)
				if tc.wantReject {
					if err == nil {
						t.Errorf("%s(%q) was accepted", op, tc.path)
					}
					if len(calls) != 0 {
						t.Errorf("%s(%q) reached contextd as %q; validation must happen before the process starts",
							op, tc.path, calls)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s(%q) was rejected: %v", op, tc.path, err)
				}
				if len(calls) != 1 {
					t.Errorf("%s(%q) produced %d contextd invocations, want 1", op, tc.path, len(calls))
				}
			}
		})
	}
}

// CheckStatus is the install-time and runtime "is Contextverse here" answer,
// and each of these shapes sends the operator somewhere different. Reporting
// available when it is not is the worst of them: memory silently does nothing
// and the UI says everything is fine.
func TestCheckStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		replies       map[string]reply
		wantAvailable bool
		wantVersion   string
		wantSpaceRoot string
		wantMode      string
		errContains   string
	}{
		{
			name: "installed with an initialized space",
			replies: map[string]reply{
				"version": {stdout: "contextd 0.4.1\n"},
				"status":  {stdout: `{"space_root":"/home/u/.contextd","exists":true,"mode":"solo"}`},
			},
			wantAvailable: true,
			// Reported as "0.4.1": the binary's own name is stripped, so the
			// UI does not read "contextd contextd 0.4.1".
			wantVersion:   "0.4.1",
			wantSpaceRoot: "/home/u/.contextd",
			wantMode:      "solo",
		},
		{
			name: "installed but no space has been created",
			replies: map[string]reply{
				"version": {stdout: "contextd 0.4.1\n"},
				"status":  {stdout: `{"space_root":"/home/u/.contextd","exists":false,"mode":"solo"}`},
			},
			wantAvailable: false,
			wantVersion:   "0.4.1",
			wantSpaceRoot: "/home/u/.contextd",
			wantMode:      "solo",
			// The exact command that fixes it, because this is the state a
			// fresh install lands in.
			errContains: "contextd init solo",
		},
		{
			name: "a contextd whose status output we cannot read",
			replies: map[string]reply{
				"version": {stdout: "contextd 9.9.9\n"},
				"status":  {stdout: "space: /home/u/.contextd (solo)\n"},
			},
			wantAvailable: false,
			wantVersion:   "9.9.9",
			errContains:   "unparseable",
		},
		{
			name: "status itself fails",
			replies: map[string]reply{
				"version": {stdout: "contextd 0.4.1\n"},
				"status":  {code: 1, stderr: "error: space is locked by another process\n"},
			},
			wantAvailable: false,
			// Still reported: "installed, but broken" is a different
			// conversation from "not installed".
			wantVersion: "0.4.1",
			errContains: "space is locked by another process",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeContextd(t, tc.replies)
			got := New(f.path).CheckStatus(context.Background())

			if got.Available != tc.wantAvailable {
				t.Errorf("Available = %v, want %v (status was %+v)", got.Available, tc.wantAvailable, got)
			}
			if got.Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tc.wantVersion)
			}
			if got.SpaceRoot != tc.wantSpaceRoot {
				t.Errorf("SpaceRoot = %q, want %q", got.SpaceRoot, tc.wantSpaceRoot)
			}
			if got.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, tc.wantMode)
			}
			if tc.errContains == "" {
				if got.Error != "" {
					t.Errorf("Error = %q, want none", got.Error)
				}
			} else if !strings.Contains(got.Error, tc.errContains) {
				t.Errorf("Error = %q, want it to contain %q", got.Error, tc.errContains)
			}
		})
	}
}

// An empty contextd_path in config.yaml has to mean "find contextd on PATH",
// which is how every packaged install (container, brew, deb) is wired. Without
// the fallback the default config produces an empty command name and context
// is dead on arrival.
func TestEmptyBinaryNameFallsBackToPATH(t *testing.T) {
	// t.Setenv, so no t.Parallel here.
	f := newFakeContextd(t, map[string]reply{"default": {stdout: `[{"path":"a.md","version":"1"}]`}})
	t.Setenv("PATH", filepath.Dir(f.path))

	got, err := New("").List(context.Background())
	if err != nil {
		t.Fatalf("New(\"\") did not resolve contextd from PATH: %v", err)
	}
	if len(got) != 1 || got[0].Path != "a.md" {
		t.Errorf("List = %+v, want the one file the binary on PATH reported", got)
	}
}
