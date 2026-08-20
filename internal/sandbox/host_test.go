package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A real shell, on a real pty, on the machine running the tests.
//
// Nothing is mocked here on purpose. The thing that would break silently is
// not the Go around it — it is whether what comes back behaves like a
// terminal at all, and the only way to know that is to type into one.
func TestAHostShellRunsWhatYouTypeAndEndsWhenYouLeave(t *testing.T) {
	if !HostTerminals() {
		t.Skip("this platform has no host terminal to open")
	}

	dir := t.TempDir()
	// A file whose name the shell can only print by having actually run in
	// this directory, so `ls` proves the working directory took effect rather
	// than the test proving its own echo.
	if err := os.WriteFile(filepath.Join(dir, "proof-of-directory"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	sh, err := (&Host{}).Session(context.Background(), 24, 80, dir)
	if err != nil {
		t.Fatalf("open a shell on this machine: %v", err)
	}
	t.Cleanup(func() { sh.Close() })

	if _, err := sh.Write([]byte("ls\n")); err != nil {
		t.Fatalf("type into the shell: %v", err)
	}
	if got := readUntil(t, sh, "proof-of-directory"); !strings.Contains(got, "proof-of-directory") {
		t.Fatalf("the shell never listed the directory it was opened in.\nWhat it said:\n%s", got)
	}

	// Resizing is what separates a terminal from a pipe: without it anything
	// full-screen draws at 80x24 forever, whatever size the window is.
	if err := sh.Resize(40, 120); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if _, err := sh.Write([]byte("tput cols\n")); err != nil {
		t.Fatal(err)
	}
	if got := readUntil(t, sh, "120"); !strings.Contains(got, "120") {
		t.Errorf("the shell did not see the new width.\nWhat it said:\n%s", got)
	}

	// Closing must actually end the process. A terminal that leaks a shell per
	// visit is a machine that runs out of them.
	if err := sh.Close(); err != nil && !strings.Contains(err.Error(), "file already closed") {
		t.Fatalf("close: %v", err)
	}
	if _, err := sh.Read(make([]byte, 1)); err == nil {
		t.Error("reading from a closed shell succeeded: the process is still there")
	}
}

// readUntil reads until want shows up or the deadline passes, returning
// everything it saw either way — the failure message needs the transcript, not
// just "timed out".
func readUntil(t *testing.T, s Session, want string) string {
	t.Helper()
	var seen strings.Builder
	deadline := time.After(15 * time.Second)
	got := make(chan []byte)
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case got <- chunk:
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case chunk := <-got:
			seen.Write(chunk)
			// The command is echoed back before it runs, so the text being
			// present is not enough — it has to appear on a line that is not
			// the one that was typed.
			if strings.Count(seen.String(), want) > 1 || afterEcho(seen.String(), want) {
				return seen.String()
			}
		case <-deadline:
			return seen.String()
		}
	}
}

// afterEcho reports whether want appears somewhere other than the first line,
// which is the shell repeating what was typed.
func afterEcho(transcript, want string) bool {
	lines := strings.Split(transcript, "\n")
	for _, line := range lines[1:] {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// The shell has to be one that exists. A fallback that names something the
// machine does not have would fail at the moment somebody opens a terminal,
// which is the worst possible time to find out.
func TestTheFallbackShellExists(t *testing.T) {
	if !HostTerminals() {
		t.Skip("this platform has no host terminal to open")
	}
	t.Setenv("SHELL", "")
	shell := loginShell()
	if _, err := os.Stat(shell); err != nil {
		t.Fatalf("loginShell picked %q with no $SHELL set, and it is not there: %v", shell, err)
	}
}
