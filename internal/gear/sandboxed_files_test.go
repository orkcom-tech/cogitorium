package gear

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
)

// The file protocol where it is actually a boundary.
//
// internal/gear/files_test.go runs the same protocol on the subprocess backend,
// where "in/ is read-only" is a file mode the gear could chmod back. Here the
// gear is a process in a container that owns nothing but out/, and the test
// asks it — from the inside — what it can and cannot do. Nothing here is
// simulated: a real container, a real uid, real refusals from the kernel.
//
// It skips rather than fails when Docker is not usable, and it builds its own
// image FROM scratch so that no registry has to be reachable for it to run.

// probeSource is a gear that reports what the sandbox allows. It is a `binary`
// gear because an image containing nothing has no interpreter to hand a script
// to — and a static binary is the one thing that runs in one.
const probeSource = `package main

import (
	"encoding/hex"
	"fmt"
	"os"
)

func say(label string, err error) { fmt.Printf("%s=%v\n", label, err) }

func main() {
	fmt.Printf("uid=%d\n", os.Geteuid())

	given, err := os.ReadFile("in/data/report.bin")
	say("read_in", err)
	fmt.Printf("hex=%s\n", hex.EncodeToString(given))

	f, err := os.OpenFile("in/data/report.bin", os.O_WRONLY, 0)
	if err == nil {
		f.Close()
	}
	say("write_in", err)
	say("create_in", os.WriteFile("in/planted.txt", []byte("x"), 0o644))
	say("remove_in", os.Remove("in/data/report.bin"))
	say("write_beside_code", os.WriteFile("planted.txt", []byte("x"), 0o644))
	say("remove_entrypoint", os.Remove("probe"))

	say("write_out", os.WriteFile("out/answer.txt", []byte("42"), 0o644))
	say("copy_out", os.WriteFile("out/copy.bin", given, 0o644))
	say("symlink_out", os.Symlink("/etc/hosts", "out/hosts_link"))
}
`

// lookerSource is the same question asked of a call that names no files.
const lookerSource = `package main

import (
	"fmt"
	"io"
	"os"
)

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func main() {
	in, _ := io.ReadAll(os.Stdin)
	fmt.Printf("stdin=%s\n", in)
	fmt.Printf("in=%v\n", exists("in"))
	fmt.Printf("out=%v\n", exists("out"))
}
`

// sandboxed is a fixture whose gears run in a container.
type sandboxed struct {
	*fixture
	goos, goarch string
}

func newSandboxed(t *testing.T) *sandboxed {
	t.Helper()
	sb := sandbox.NewDocker("", "")
	if sb == nil {
		t.Skip("docker is not installed; the sandboxed boundary cannot be exercised here")
	}
	if !sb.Available(context.Background()) {
		t.Skip("the docker daemon does not answer; the sandboxed boundary cannot be exercised here")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build a binary gear with")
	}

	// FROM scratch: the image holds one byte, because everything the run needs
	// arrives in the payload. It also means this test needs no registry — the
	// difference between a test that runs and one that is always skipped on a
	// machine with no route to a registry.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "placeholder"), []byte("x"))
	write(t, filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY placeholder /placeholder\n"))
	tag := "cogitorium-test-scratch:" + strings.ToLower(t.Name())
	if out, err := exec.Command("docker", "build", "-q", "-t", tag, dir).CombinedOutput(); err != nil {
		t.Skipf("cannot build the test image: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })

	s := &sandboxed{
		fixture: newFixture(t),
		// The container's OS and architecture, not this machine's: a macOS build
		// does not run in a Linux container, and neither does an amd64 one on an
		// arm64 daemon.
		goos:   dockerInfo(t, "{{.Server.Os}}"),
		goarch: dockerInfo(t, "{{.Server.Arch}}"),
	}
	s.exec.sandbox = sandbox.NewDocker(tag, "")

	// Rebind the gate where a CONTAINER can reach it, by the same call the
	// server makes at startup. The fixture's default is the loopback, which a
	// container reaches on Docker Desktop and cannot reach on Linux — so a test
	// that kept it would pass on the machine this was written on and fail on
	// the machine it runs on, which is exactly what happened.
	s.gate.Close()
	gate, err := gearnet.New(s.db, gearnet.ListenFor(context.Background(), "", s.exec.sandbox), t.TempDir())
	if err != nil {
		t.Fatalf("open a gate a container can reach: %v", err)
	}
	t.Cleanup(func() { gate.Close() })
	s.gate = gate
	s.exec.gate = gate
	return s
}

// approveBinary compiles source for the container and forges it as an approved
// binary gear, which is how a compiled tool reaches an agent.
func (s *sandboxed) approveBinary(name, entrypoint, source string) Gear {
	s.t.Helper()
	return s.approveBinaryWithEnv(name, entrypoint, source, nil)
}

// approveBinaryWithEnv is the same, for a gear that asks to be given named
// values — the declaration an operator reads at approval.
func (s *sandboxed) approveBinaryWithEnv(name, entrypoint, source string, envNames []string) Gear {
	s.t.Helper()
	src := s.t.TempDir()
	write(s.t, filepath.Join(src, "main.go"), []byte(source))
	write(s.t, filepath.Join(src, "go.mod"), []byte("module probe\n\ngo 1.25\n"))
	bin := filepath.Join(src, entrypoint)

	build := exec.Command("go", "build", "-trimpath", "-o", bin, ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+s.goos, "GOARCH="+s.goarch)
	if out, err := build.CombinedOutput(); err != nil {
		s.t.Skipf("cannot cross-build a gear for %s/%s: %v: %s", s.goos, s.goarch, err, out)
	}
	body, err := os.ReadFile(bin)
	if err != nil {
		s.t.Fatal(err)
	}

	ctx := context.Background()
	g, err := s.gears.Forge(ctx, name, "a compiled test gear", nil, RuntimeBinary, entrypoint, "", envNames,
		[]File{{Path: entrypoint, Content: base64.StdEncoding.EncodeToString(body), Encoding: EncodingBase64}},
		s.wsID, s.agentID)
	if err != nil {
		s.t.Fatalf("forge %q: %v", name, err)
	}
	g, err = s.gears.SetStatus(ctx, g.ID, StatusApproved, Actor{Name: "test-operator"})
	if err != nil {
		s.t.Fatalf("approve %q: %v", name, err)
	}
	return g
}

func dockerInfo(t *testing.T, format string) string {
	t.Helper()
	out, err := exec.Command("docker", "version", "--format", format).Output()
	if err != nil {
		t.Skipf("cannot ask docker what it runs: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// 1, 2, 4 and 5, in the sandbox: the gear reads its input exactly, cannot
// change it or anything else it was given, writes out/ freely, and what it
// leaves there comes back into the workspace with the symlink refused.
func TestSandboxedGearOwnsOutAndNothingElse(t *testing.T) {
	s := newSandboxed(t)
	s.put("data/report.bin", payload)
	g := s.approveBinary("probe", "probe", probeSource)

	res, err := s.run(g, `{}`, []string{"data/report.bin"})
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}
	said := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			said[k] = v
		}
	}
	t.Logf("the gear reported:\n%s", strings.TrimSpace(res.Stdout))

	// The uid the payload's ownership is written for. A container running as
	// anybody else makes every claim below meaningless.
	if said["uid"] != "65534" {
		t.Fatalf("the gear ran as uid %q, and the payload is owned by 65534", said["uid"])
	}

	// 1. It reads what it was given, exactly.
	if said["read_in"] != "<nil>" {
		t.Errorf("the gear could not read in/data/report.bin: %s", said["read_in"])
	}
	if said["hex"] != hex.EncodeToString(payload) {
		t.Errorf("the gear read different bytes than the workspace holds\n got %s\nwant %s", said["hex"], hex.EncodeToString(payload))
	}

	// 4. in/ is not writable, and neither is the code beside it. The directory
	// matters as much as the file: a process that can create entries in in/ has
	// a writable input directory whatever the modes of the files in it say.
	for _, refused := range []string{"write_in", "create_in", "remove_in", "write_beside_code", "remove_entrypoint"} {
		if said[refused] == "<nil>" {
			t.Errorf("%s succeeded; out/ is the only thing a gear given files may change", refused)
			continue
		}
		if !strings.Contains(said[refused], "permission denied") {
			t.Errorf("%s failed for some other reason than permission: %s", refused, said[refused])
		}
	}
	if got := s.read("data/report.bin"); string(got) != string(payload) {
		t.Errorf("the workspace's own file changed under the gear: %q", got)
	}

	// 4. out/ is.
	for _, allowed := range []string{"write_out", "copy_out", "symlink_out"} {
		if said[allowed] != "<nil>" {
			t.Errorf("%s was refused, and out/ is the gear's own: %s", allowed, said[allowed])
		}
	}

	// 2 and 5. What it wrote comes back; what it linked does not, and is named.
	byName := map[string]Produced{}
	for _, p := range res.Produced {
		byName[p.Name] = p
	}
	if len(byName) != 2 {
		t.Fatalf("collected %+v, want answer.txt and copy.bin (not taken: %v)", res.Produced, res.Ignored)
	}
	if got := s.read(byName["copy.bin"].Path); string(got) != string(payload) {
		t.Errorf("the file the gear copied came back changed:\n got %x\nwant %x", got, payload)
	}
	if got := s.read(byName["answer.txt"].Path); string(got) != "42" {
		t.Errorf("out/answer.txt came back as %q", got)
	}
	if _, taken := byName["hosts_link"]; taken {
		t.Error("a symlink the gear made in out/ was followed into the workspace")
	}
	if report := strings.Join(res.Ignored, "\n"); !strings.Contains(report, "hosts_link") || !strings.Contains(report, "symlink") {
		t.Errorf("the caller was not told why out/hosts_link did not arrive: %q", report)
	}
}

// 3, in the sandbox: a call that names no files gets no in/, no out/, and the
// bytes it has always got on stdin.
func TestSandboxedGearWithNoFilesSeesNeitherDirectory(t *testing.T) {
	s := newSandboxed(t)
	g := s.approveBinary("looker", "look", lookerSource)

	args := `{"z": 1,  "a":"x",   "n":1.50}`
	res, err := s.run(g, args, nil)
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}
	if want := fmt.Sprintf("stdin=%s\nin=false\nout=false\n", args); res.Stdout != want {
		t.Errorf("a call that named no files saw\n %q\nwant %q", res.Stdout, want)
	}
	if res.Produced != nil || res.Ignored != nil {
		t.Errorf("a file-less run reported files: produced %v, not taken %v", res.Produced, res.Ignored)
	}
}
