package gear

import (
	"context"
	"database/sql"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The file protocol, proved by running gears rather than by reading the code
// that starts them.
//
// The backend here is the unsandboxed subprocess: it is what an install without
// Docker actually runs, and the only backend a test can require to be present.
// Where a rule is enforced by the sandbox instead of by this package — a
// directory the gear cannot create files in at all — it is proved where it is
// enforced, in internal/sandbox/payload_test.go, because a test that asserted
// it here would be asserting something this backend does not do.

// payload is deliberately not text: a NUL, a byte that is not valid UTF-8, a
// CRLF and a multi-byte rune. "Reads its bytes exactly" is only worth testing
// against bytes that something in the way would mangle.
var payload = []byte("first\r\nline\x00\x01\x02 ünïcödé …\xff\xfe done")

// fixture is a real executor over a real SQLite database, with a real workspace
// directory on disk and a real agent to run on behalf of.
type fixture struct {
	t     *testing.T
	exec  *Executor
	gears *Store
	// db and gate are kept so a test can ask the same database and the same
	// outward gate the executor is using what actually happened — the whole
	// point of a real one being wired in rather than a stub.
	db      *sql.DB
	gate    *gearnet.Gate
	dataDir string
	root    string // the workspace's own directory
	outside string // a directory that is NOT the workspace, for the escape tests
	wsID    int64
	agentID int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable; the subprocess backend has nothing to run")
	}
	ctx := context.Background()
	dataDir := t.TempDir()

	db, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	owner, _, err := identity.NewStore(db).CreateUser(ctx, "operator", "member", "")
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	cat := catalog.NewStore(db)
	p, err := cat.CreateProvider(ctx, "anthropic", llm.TypeAnthropic, "", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m, err := cat.CreateModel(ctx, p.ID, "claude-sonnet-4-6", "")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	ws := workspace.NewStore(db)
	space, err := ws.CreateWorkspace(ctx, "atlas", "", m.ID, owner.ID)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	agent, err := ws.CreateAgentSpec(ctx, space.ID, workspace.AgentSpec{
		Name: "worker", Role: "You work.", ModelID: &m.ID,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	root := workdir.Dir(dataDir, space.ID)
	if root == "" {
		t.Fatal("the workspace has no working directory, so nothing below can run")
	}
	gate := testGate(t, db)
	return &fixture{
		t: t, exec: NewExecutor(NewStore(db), dataDir, nil, testResolver(t, db), gate), gears: NewStore(db),
		db: db, gate: gate,
		dataDir: dataDir, root: root, outside: t.TempDir(),
		wsID: space.ID, agentID: agent.ID,
	}
}

// testResolver builds the real lookup over the test's own database: no
// encryption key and no mounted directories, which is the ordinary shape of an
// install that has never set one. A gear declaring no names never reaches it.
func testResolver(t *testing.T, db *sql.DB) *secrets.Resolver {
	t.Helper()
	r, err := secrets.NewResolver(secrets.NewStore(db, nil), "", "")
	if err != nil {
		t.Fatalf("build the named-value resolver: %v", err)
	}
	return r
}

// testGate opens a real outward gate on a real loopback port over the test's
// own database. No gear here is granted the network, so nothing reaches it —
// but a nil gate would be a different executor from the one that ships, and the
// difference is exactly the branch that refuses a granted gear.
func testGate(t *testing.T, db *sql.DB) *gearnet.Gate {
	t.Helper()
	g, err := gearnet.New(db, "127.0.0.1:0", t.TempDir())
	if err != nil {
		t.Fatalf("open the gear network gate: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// approve forges a bash gear and approves it, which is the only state an agent
// can call one in.
func (f *fixture) approve(name, code string) Gear {
	f.t.Helper()
	ctx := context.Background()
	g, err := f.gears.Forge(ctx, name, "a test gear", nil, "bash", "main.sh", "", nil,
		[]File{{Path: "main.sh", Content: code}}, f.wsID, f.agentID)
	if err != nil {
		f.t.Fatalf("forge %q: %v", name, err)
	}
	g, err = f.gears.SetStatus(ctx, g.ID, StatusApproved)
	if err != nil {
		f.t.Fatalf("approve %q: %v", name, err)
	}
	return g
}

// put writes a file into the workspace, the way an inlet delivery or the
// operator's Files page would.
func (f *fixture) put(rel string, content []byte) {
	f.t.Helper()
	full := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) run(g Gear, argsJSON string, files []string) (Result, error) {
	f.t.Helper()
	return f.exec.RunWithFiles(context.Background(), g, argsJSON, files,
		Caller{AgentID: &f.agentID, WorkspaceID: &f.wsID})
}

// read returns a workspace file's bytes, failing the test if it is not there.
func (f *fixture) read(rel string) []byte {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("read %q from the workspace: %v", rel, err)
	}
	return b
}

// snapshot lists every path under dir with its size, so a test can prove a
// hostile call left a tree exactly as it found it.
func snapshot(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		size := int64(-1)
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			size = info.Size()
		}
		out[rel] = size
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

func sameTree(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// find returns every path under dir whose base name matches, so a test can ask
// "did this file appear anywhere at all".
func find(t *testing.T, dir, name string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if filepath.Base(p) == name {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// 1. A gear given a file opens it at in/<the same path> and reads its bytes
// exactly. The gear names the path as a literal, so a change to where a file is
// staged fails here rather than being discovered by an agent; and it hexdumps
// what it read, so the assertion is about bytes and not about whether something
// in the way survived a NUL.
func TestAGearReadsItsGivenFileByteForByte(t *testing.T) {
	f := newFixture(t)
	f.put("data/report.bin", payload)

	// pipefail so a gear that cannot open its input fails loudly here, rather
	// than passing tr's exit status off as its own.
	g := f.approve("reader", "set -euo pipefail\nod -An -v -tx1 in/data/report.bin | tr -d ' \\n'\n")
	res, err := f.run(g, `{}`, []string{"data/report.bin"})
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("the gear could not read in/data/report.bin: exit %d, stderr %q", res.ExitCode, res.Stderr)
	}
	if got, want := strings.TrimSpace(res.Stdout), hex.EncodeToString(payload); got != want {
		t.Errorf("the gear read different bytes than the workspace holds\n got %s\nwant %s", got, want)
	}
}

// 2. Whatever the gear writes to out/ lands in the workspace, and the caller is
// told what appeared — with the path it can hand to the next gear.
func TestWhatAGearWritesToOutLandsInTheWorkspace(t *testing.T) {
	f := newFixture(t)
	f.put("data/report.bin", payload)

	g := f.approve("copier", "set -eu\nmkdir -p out/notes\ncp in/data/report.bin out/notes/copy.bin\nprintf 'the answer' > out/top.txt\n")
	res, err := f.run(g, `{}`, []string{"data/report.bin"})
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}

	byName := map[string]Produced{}
	for _, p := range res.Produced {
		byName[p.Name] = p
	}
	if len(byName) != 2 {
		t.Fatalf("the caller was told about %d files, want 2: %+v (not taken: %v)", len(byName), res.Produced, res.Ignored)
	}
	for name, want := range map[string][]byte{
		"notes/copy.bin": payload,
		"top.txt":        []byte("the answer"),
	} {
		p, ok := byName[name]
		if !ok {
			t.Errorf("out/%s was not reported to the caller", name)
			continue
		}
		if got := f.read(p.Path); string(got) != string(want) {
			t.Errorf("%s holds %q, want %q", p.Path, got, want)
		}
		if p.Bytes != int64(len(want)) {
			t.Errorf("%s was reported as %d bytes, and it is %d", p.Path, p.Bytes, len(want))
		}
		if p.Replaced {
			t.Errorf("%s was reported as replacing something, in a directory made for this run", p.Path)
		}
		// The reported path is workspace-relative and lands under the gear's own
		// run directory: that is the form the caller passes back in.
		if !strings.HasPrefix(p.Path, workdir.GearOutDir+"/"+g.Name+"/") {
			t.Errorf("%s is not under %s/%s/, so it is not where the caller was told to look",
				p.Path, workdir.GearOutDir, g.Name)
		}
	}

	// A workspace's files have no business staying in the gear directory once
	// the run that borrowed them is over.
	if left, _ := filepath.Glob(filepath.Join(f.dataDir, "gears", g.Name, "v*.run-*")); len(left) > 0 {
		t.Errorf("the run directory was left behind: %v", left)
	}
}

// 3. A gear that names no files sees no in/ and no out/, and gets on stdin
// exactly what it always got. The same gear is run both ways and the two
// answers are compared, because "unchanged" is a claim about a difference.
func TestAGearThatNamesNoFilesRunsExactlyAsBefore(t *testing.T) {
	f := newFixture(t)
	f.put("data/report.bin", payload)

	g := f.approve("echoer", "set -eu\ncat\nprintf '\\n---\\n'\nif [ -e in ]; then echo in=yes; else echo in=no; fi\nif [ -e out ]; then echo out=yes; else echo out=no; fi\n")

	// The bytes a model produced, with the keys in an order no re-encoding
	// would keep and whitespace no re-encoding would preserve.
	args := `{"z": 1,  "a":"x",   "n":1.50}`

	before, err := f.run(g, args, nil)
	if err != nil {
		t.Fatalf("run without files: %v", err)
	}
	withFiles, err := f.run(g, args, []string{"data/report.bin"})
	if err != nil {
		t.Fatalf("run with files: %v", err)
	}

	stdin, markers, ok := strings.Cut(before.Stdout, "\n---\n")
	if !ok {
		t.Fatalf("the gear's output does not have the shape this test reads: %q", before.Stdout)
	}
	if stdin != args {
		t.Errorf("stdin was rewritten on the way to the gear\n got %q\nwant %q", stdin, args)
	}
	if want := "in=no\nout=no\n"; markers != want {
		t.Errorf("a call that named no files saw %q, want %q", markers, want)
	}

	// And the same call through the older door: RunStream is what the operator's
	// dry run and the terminal use, and it must not have drifted from this one.
	legacy, err := f.exec.RunStream(context.Background(), g, args, Caller{AgentID: &f.agentID, WorkspaceID: &f.wsID}, nil)
	if err != nil {
		t.Fatalf("legacy run: %v", err)
	}
	if legacy.Stdout != before.Stdout {
		t.Errorf("the two doors into a file-less run disagree\n RunWithFiles(nil): %q\n RunStream:         %q", before.Stdout, legacy.Stdout)
	}
	if legacy.Produced != nil || legacy.Ignored != nil {
		t.Errorf("a file-less run reported files: produced %v, not taken %v", legacy.Produced, legacy.Ignored)
	}

	// The file-carrying run is the control: the same gear, the same arguments,
	// and the only difference is the protocol it asked for.
	stdin2, markers2, _ := strings.Cut(withFiles.Stdout, "\n---\n")
	if stdin2 != args {
		t.Errorf("a file-carrying call also rewrote stdin\n got %q\nwant %q", stdin2, args)
	}
	if want := "in=yes\nout=yes\n"; markers2 != want {
		t.Errorf("a call that named a file saw %q, want %q", markers2, want)
	}
}

// 4. in/ is not writable by the gear; out/ is.
//
// On this backend that is the file mode and nothing else, and the code says so:
// the process runs as the account that owns those files and could chmod them
// back. What is being proved here is that an honest gear writing to its input
// FAILS instead of silently corrupting what it was handed — the boundary itself
// is the sandbox's, and internal/sandbox/payload_test.go proves that in/ is
// root's and only out/ is the sandbox user's.
func TestInIsNotWritableAndOutIs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file modes stop nothing, so this proves nothing")
	}
	f := newFixture(t)
	f.put("data/report.bin", payload)

	g := f.approve("prober", "set -u\n"+
		"if printf 'x' >> in/data/report.bin 2>/dev/null; then echo in=writable; else echo in=refused; fi\n"+
		"if printf 'ok' > out/ok.txt 2>/dev/null; then echo out=writable; else echo out=refused; fi\n")
	res, err := f.run(g, `{}`, []string{"data/report.bin"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "in=refused\nout=writable\n"; res.Stdout != want {
		t.Errorf("the gear reported %q, want %q", res.Stdout, want)
	}
	if got := f.read("data/report.bin"); string(got) != string(payload) {
		t.Errorf("the workspace's own file changed under the gear: %q", got)
	}
	if len(res.Produced) != 1 || res.Produced[0].Name != "ok.txt" {
		t.Errorf("out/ok.txt did not come back: produced %+v, not taken %v", res.Produced, res.Ignored)
	}
}

// 5a. Hostile paths on the way IN. Every case is refused or contained, and the
// secret sitting outside the workspace is never opened.
//
// "Refused" is the right word for three of these and the wrong one for the
// fourth: workdir.Clean collapses ".." rather than rejecting it, so "../x"
// means "x in this workspace" and is answered by the workspace's own file if
// there is one. That is containment, not refusal, and it is asserted as what it
// is — see the sibling test below.
func TestHostileFileArgumentsAreRefused(t *testing.T) {
	f := newFixture(t)
	secret := filepath.Join(f.outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("provider api key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(f.root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	g := f.approve("greedy", "set -eu\nfind in -type f | sort\ncat in/* 2>/dev/null || true\n")
	outsideBefore := snapshot(t, f.outside)
	wsBefore := snapshot(t, f.root)

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"a symlink out of the workspace", "link.txt", "leaves the workspace"},
		{"an absolute path", "/etc/hosts", `there is no "etc/hosts"`},
		{"a relative climb", "../secret.txt", `there is no "secret.txt"`},
		{"fine until it is cleaned", "data/../../../../etc/hosts", `there is no "etc/hosts"`},
		// Caught by the stat rather than by the containment proof, and worth
		// knowing why: filepath.EvalSymlinks walks from the left and stops at
		// the first component that does not exist, so it never reaches the NUL
		// to complain about it. The path is inside the workspace, it is simply
		// not openable — which is a refusal either way, and the one thing that
		// must not happen is a truncated open of "data/".
		{"a NUL byte", "data/\x00/etc/hosts", "cannot be read"},
		{"the workspace itself", ".", "names the workspace itself"},
	} {
		res, err := f.run(g, `{}`, []string{tc.path})
		if err == nil {
			t.Errorf("%s: %q was accepted; the gear saw %q", tc.name, tc.path, res.Stdout)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refused with %q, which does not say %q", tc.name, err, tc.want)
		}
		if strings.Contains(err.Error(), "provider api key") {
			t.Errorf("%s: the refusal quotes the secret it refused to open: %v", tc.name, err)
		}
		if res.Stdout != "" || res.Produced != nil {
			t.Errorf("%s: the gear ran anyway: stdout %q, produced %+v", tc.name, res.Stdout, res.Produced)
		}
	}

	if !sameTree(outsideBefore, snapshot(t, f.outside)) {
		t.Errorf("a refused call changed something outside the workspace")
	}
	if !sameTree(wsBefore, snapshot(t, f.root)) {
		t.Errorf("a refused call changed the workspace")
	}
	if left, _ := filepath.Glob(filepath.Join(f.dataDir, "gears", g.Name, "v*.run-*")); len(left) > 0 {
		t.Errorf("a refused call left its run directory behind: %v", left)
	}
}

// 5a, continued. ".." is clamped, not rejected: "../secret.txt" is read as
// "secret.txt" in this workspace. The gear gets the workspace's own file and
// never the one a directory above it — which is the property that matters, and
// is stated here rather than left as a surprise.
func TestARelativeClimbIsClampedToTheWorkspace(t *testing.T) {
	f := newFixture(t)
	// Deliberately the parent of the workspace directory: the closest thing an
	// escape would reach, and where the server's own database lives one level up.
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.root), "secret.txt"), []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.put("secret.txt", []byte("INSIDE"))

	g := f.approve("reader2", "set -eu\ncat in/secret.txt\n")
	res, err := f.run(g, `{}`, []string{"../secret.txt"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stdout != "INSIDE" {
		t.Errorf("the gear was handed %q; \"../secret.txt\" must mean this workspace's own file", res.Stdout)
	}
}

// 5b. Hostile paths on the way OUT. The gear owns out/ and writes what it
// likes; only regular files under it are taken, everything else is named in the
// report, and nothing lands outside the workspace.
//
// A NUL byte does not appear here because a gear cannot write one: a filename
// containing NUL cannot be created by any syscall on this platform. It is
// tested where it can arrive — as a file ARGUMENT, above.
func TestHostileOutputIsNotCollected(t *testing.T) {
	f := newFixture(t)
	g := f.approve("naughty", "set -u\n"+
		"printf 'escaped' > out/../escape.txt 2>/dev/null || true\n"+
		"ln -s /etc/hosts out/hosts_link 2>/dev/null || true\n"+
		"ln -s ../../../.. out/up 2>/dev/null || true\n"+
		"mkfifo out/pipe 2>/dev/null || true\n"+
		"mkdir -p out/sub && printf 'kept' > out/sub/kept.txt\n"+
		"printf 'clean' > out/./tidy.txt\n"+
		"echo done\n")

	outsideBefore := snapshot(t, f.outside)
	res, err := f.run(g, `{}`, []string{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var got []string
	for _, p := range res.Produced {
		got = append(got, p.Name)
		if p.Name == "sub/kept.txt" {
			if b := f.read(p.Path); string(b) != "kept" {
				t.Errorf("%s holds %q, want \"kept\"", p.Path, b)
			}
		}
	}
	sort.Strings(got)
	if want := []string{"sub/kept.txt", "tidy.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("collected %v, want %v (not taken: %v)", got, want, res.Ignored)
	}

	report := strings.Join(res.Ignored, "\n")
	for _, refused := range []string{"hosts_link", "up", "pipe"} {
		if !strings.Contains(report, refused) {
			t.Errorf("out/%s was neither taken nor reported; the caller cannot know it went missing.\nreport: %s", refused, report)
		}
	}
	if !strings.Contains(report, "symlink") {
		t.Errorf("the report does not say a symlink was refused, so it does not say why: %s", report)
	}

	if hits := find(t, f.root, "escape.txt"); len(hits) > 0 {
		t.Errorf("a file written to out/../ landed in the workspace: %v", hits)
	}
	if hits := find(t, f.dataDir, "escape.txt"); len(hits) > 0 {
		t.Errorf("a file written to out/../ outlived its run: %v", hits)
	}
	if hits := find(t, f.root, "hosts_link"); len(hits) > 0 {
		t.Errorf("a symlink out of the container reached the workspace: %v", hits)
	}
	if !sameTree(outsideBefore, snapshot(t, f.outside)) {
		t.Errorf("the run changed something outside the workspace")
	}
}
