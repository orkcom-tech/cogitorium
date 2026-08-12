package gear

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/secrets"
)

// Named values where they are actually a boundary: inside a running gear.
//
// Every claim here is asked of the process rather than of the code that starts
// it. What a gear was given is what it can read from its own environment, so
// the gear dumps that environment into out/ and the test reads it out of the
// workspace — a Spec, a map or a resolved slice would only prove that this
// package believes what it wrote down.
//
// The backend is the unsandboxed subprocess, for the same reason files_test.go
// uses it: it is what an install without Docker runs, and it is the only
// backend a test can require to be present. The wiring the sandbox adds —
// --network none, the container's own environment — is proved where it is
// enforced, in sandboxed_network_test.go.

// testSecretKey stands in for COGITORIUM_SECRET_KEY. Long enough to be
// accepted, and nothing else about it matters: it is derived, not used.
const testSecretKey = "a-test-key-that-is-comfortably-longer-than-the-floor"

// The values these tests hunt for. Each is distinctive enough that finding it
// anywhere is unambiguous, and none contains a character that would make it an
// illegal file name — one of the gears below names a file after its own
// credential, because a gear choosing that name is a way a value reaches the
// chat without ever being printed.
const (
	storedSecret  = "sk-live-ledger-9f2c4e7a-never-publish-this"
	mountedSecret = "sk-mount-3b81d05e-never-publish-this"
	plainVariable = "https://ledger.example.com/public-endpoint"
)

// named is a fixture whose install has all four sources a name can come from:
// its own store (global and per-workspace), a mounted variables directory and a
// mounted secrets directory. It holds an encryption key, so a secret can live
// in the database as well as on disk.
type named struct {
	*fixture
	store        *secrets.Store
	variablesDir string
	secretsDir   string
}

func newNamed(t *testing.T) *named {
	t.Helper()
	f := newFixture(t)
	n := &named{fixture: f, variablesDir: t.TempDir(), secretsDir: t.TempDir()}

	key, err := secrets.NewKey(testSecretKey)
	if err != nil {
		t.Fatalf("derive the test secret key: %v", err)
	}
	n.store = secrets.NewStore(f.db, key)
	resolver, err := secrets.NewResolver(n.store, n.variablesDir, n.secretsDir)
	if err != nil {
		t.Fatalf("build the named-value resolver: %v", err)
	}
	// The executor the server builds, over this install's four sources. Rebuilt
	// rather than reached into, so nothing here can be given a lookup the
	// shipping constructor would not have produced.
	f.exec = NewExecutor(f.gears, f.dataDir, nil, resolver, f.gate)
	return n
}

// global sets a name install-wide: source one.
func (n *named) global(name, kind, value string) {
	n.t.Helper()
	if _, err := n.store.Set(context.Background(), nil, name, kind, value, ""); err != nil {
		n.t.Fatalf("set %s install-wide: %v", name, err)
	}
}

// forWorkspace sets this workspace's own override: source four.
func (n *named) forWorkspace(name, kind, value string) {
	n.t.Helper()
	if _, err := n.store.Set(context.Background(), &n.wsID, name, kind, value, ""); err != nil {
		n.t.Fatalf("set %s for workspace %d: %v", name, n.wsID, err)
	}
}

// mountVariable writes a file into the variables directory, the way a
// Kubernetes ConfigMap arrives: source two.
func (n *named) mountVariable(name, value string) {
	n.t.Helper()
	write(n.t, filepath.Join(n.variablesDir, name), []byte(value))
}

// mountSecret is the same for the secrets directory: source three.
func (n *named) mountSecret(name, value string) {
	n.t.Helper()
	write(n.t, filepath.Join(n.secretsDir, name), []byte(value))
}

// naming forges an approved gear that asks to be given envNames — the
// declaration an operator reads at approval.
func (n *named) naming(name, code string, envNames ...string) Gear {
	n.t.Helper()
	ctx := context.Background()
	g, err := n.gears.Forge(ctx, name, "a test gear", nil, "bash", "main.sh", "", envNames,
		[]File{{Path: "main.sh", Content: code}}, n.wsID, n.agentID)
	if err != nil {
		n.t.Fatalf("forge %q: %v", name, err)
	}
	g, err = n.gears.SetStatus(ctx, g.ID, StatusApproved)
	if err != nil {
		n.t.Fatalf("approve %q: %v", name, err)
	}
	return g
}

// dumpEnv is a gear that writes down what it was actually given.
//
// out/ is the gear's own output and lands in the workspace unredacted, which is
// precisely why it can serve as the ground truth here: it is the gear putting a
// value somewhere itself, which is the one thing the plan says cannot be
// prevented. Everything the SERVER publishes is asserted separately, and must
// not contain what this file does.
const dumpEnv = "set -eu\nenv > out/environment\n"

// given runs the gear and returns the environment its process actually had.
func (n *named) given(g Gear) (map[string]string, Result) {
	n.t.Helper()
	res, err := n.run(g, `{}`, []string{})
	if err != nil {
		n.t.Fatalf("run %q: %v (stderr: %s)", g.Name, err, res.Stderr)
	}
	return n.environment(res), res
}

// environment finds the dump the gear left and parses it.
func (n *named) environment(res Result) map[string]string {
	n.t.Helper()
	path := ""
	for _, p := range res.Produced {
		if p.Name == "environment" {
			path = p.Path
		}
	}
	if path == "" {
		n.t.Fatalf("the gear left no environment dump behind: produced %+v, not taken %v", res.Produced, res.Ignored)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(n.read(path)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// logs captures what the server writes about a run, so a test can search it the
// same way it searches everything else. slog's default is process-wide and
// these tests do not run in parallel, so it is swapped and put back.
func logs(t *testing.T) func() string {
	t.Helper()
	buf := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf.String
}

// lockedBuffer exists because the gate and the executor log from goroutines of
// their own; a plain bytes.Buffer here would be a data race rather than a test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// absent fails naming the place, the needle and what was around it. A test that
// searches has to say WHERE it found the thing, or the failure is a riddle.
func absent(t *testing.T, where, haystack string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		i := strings.Index(haystack, needle)
		if i < 0 {
			continue
		}
		from := max(i-60, 0)
		to := min(i+len(needle)+60, len(haystack))
		t.Errorf("%s carries a secret's value:\n…%s…", where, haystack[from:to])
	}
}

// dbBytes is the database as it sits on disk, WAL included. A value that is
// supposed to be encrypted at rest can be searched for here, and an encryption
// that quietly stopped happening shows up as plaintext in the file.
func dbBytes(t *testing.T, dataDir string) string {
	t.Helper()
	var all strings.Builder
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(filepath.Join(dataDir, "cogitorium.db"+suffix))
		if err != nil {
			continue
		}
		all.Write(b)
	}
	if all.Len() == 0 {
		t.Fatal("the database file could not be read, so nothing below searched anything")
	}
	return all.String()
}

// 1. The order, proved by what the gear received rather than by reading
// layers().
//
// Four names, one per rung of the ladder, all resolved in a single run. Each is
// set at its own rung and at every rung below it, so a source that stops
// winning is named by the value that turned up instead — "the-store-won" in
// LADDER_WORKSPACE says the workspace override was skipped, and says it in one
// line.
func TestTheNarrowestSourceWins(t *testing.T) {
	n := newNamed(t)

	// Rung one: nothing above it.
	n.global("LADDER_STORE", secrets.KindVariable, "the-store-won")

	// Rung two: the variables directory over the store.
	n.global("LADDER_VARIABLES", secrets.KindVariable, "the-store-lost")
	n.mountVariable("LADDER_VARIABLES", "the-variables-directory-won")

	// Rung three: the secrets directory over both.
	n.global("LADDER_SECRETS", secrets.KindVariable, "the-store-lost")
	n.mountVariable("LADDER_SECRETS", "the-variables-directory-lost")
	n.mountSecret("LADDER_SECRETS", "the-secrets-directory-won")

	// Rung four: the workspace's own override over everything.
	n.global("LADDER_WORKSPACE", secrets.KindVariable, "the-store-lost")
	n.mountVariable("LADDER_WORKSPACE", "the-variables-directory-lost")
	n.mountSecret("LADDER_WORKSPACE", "the-secrets-directory-lost")
	n.forWorkspace("LADDER_WORKSPACE", secrets.KindVariable, "the-workspace-won")

	g := n.naming("ladder", dumpEnv,
		"LADDER_STORE", "LADDER_VARIABLES", "LADDER_SECRETS", "LADDER_WORKSPACE")
	env, _ := n.given(g)

	for name, want := range map[string]string{
		"LADDER_STORE":     "the-store-won",
		"LADDER_VARIABLES": "the-variables-directory-won",
		"LADDER_SECRETS":   "the-secrets-directory-won",
		"LADDER_WORKSPACE": "the-workspace-won",
	} {
		if got := env[name]; got != want {
			t.Errorf("the gear was given %s=%q; %q is the source that should have won", name, got, want)
		}
	}

	// And the same order is what the approval screen is told, in the same
	// words, so an operator reading "which source wins" is not reading a second
	// implementation of the question.
	statuses, err := n.exec.env.Describe(context.Background(), &n.wsID,
		[]string{"LADDER_STORE", "LADDER_VARIABLES", "LADDER_SECRETS", "LADDER_WORKSPACE"})
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	sources := map[string]string{}
	for _, st := range statuses {
		if !st.Found {
			t.Errorf("the approval screen says nothing supplies %s, and a gear just ran on it", st.Name)
		}
		sources[st.Name] = st.Source
	}
	for name, want := range map[string]string{
		"LADDER_STORE":     "this install's store",
		"LADDER_VARIABLES": "the variables directory",
		"LADDER_SECRETS":   "the secrets directory",
		"LADDER_WORKSPACE": "workspace",
	} {
		if !strings.Contains(sources[name], want) {
			t.Errorf("the approval screen says %s comes from %q, and the run took it from %q", name, sources[name], want)
		}
	}
}

// 2. A gear is given the names it declared and no others.
//
// Three names exist; one is declared. The other two must not be in the
// environment under any spelling, so their VALUES are searched for as well as
// their names — a leak that renamed itself on the way through would otherwise
// pass. The server's own environment is checked too: that is where the provider
// API keys live on a machine that keeps them there.
func TestAGearIsGivenOnlyTheNamesItDeclared(t *testing.T) {
	t.Setenv("SERVER_ONLY_VALUE", "the-servers-own-environment-must-not-travel")

	n := newNamed(t)
	n.global("DECLARED_ONE", secrets.KindVariable, "the-one-it-asked-for")
	n.global("UNDECLARED_TWO", secrets.KindVariable, "the-second-value-nobody-declared")
	n.global("UNDECLARED_THREE", secrets.KindSecret, "the-third-value-nobody-declared")

	g := n.naming("narrow", dumpEnv, "DECLARED_ONE")
	env, _ := n.given(g)

	if got := env["DECLARED_ONE"]; got != "the-one-it-asked-for" {
		t.Fatalf("the gear was given DECLARED_ONE=%q; the whole test rests on it having arrived", got)
	}
	for _, name := range []string{"UNDECLARED_TWO", "UNDECLARED_THREE", "SERVER_ONLY_VALUE"} {
		if got, ok := env[name]; ok {
			t.Errorf("the gear was given %s=%q, and it declared only DECLARED_ONE", name, got)
		}
	}

	// The whole dump, not only the names above: a value that arrived under a
	// different name is the same leak.
	var whole strings.Builder
	for k, v := range env {
		whole.WriteString(k + "=" + v + "\n")
	}
	absent(t, "the gear's environment", whole.String(),
		"the-second-value-nobody-declared",
		"the-third-value-nobody-declared",
		"the-servers-own-environment-must-not-travel")
}

// 3. A name nothing supplies stops the run, names itself, and leaves nothing
// behind — not a recorded run, not a materialised gear directory, not a file.
//
// The empty string is the alternative this replaces, and it is worse: it fails
// inside somebody's HTTP client with a 401 and no hint that the credential was
// never there.
func TestANameNothingSuppliesStopsTheRunAndNamesIt(t *testing.T) {
	n := newNamed(t)
	n.global("PRESENT_ONE", secrets.KindVariable, "this-one-is-set")

	g := n.naming("needy", "set -eu\nprintf 'the gear ran' > out/ran.txt\necho the gear ran\n",
		"PRESENT_ONE", "MISSING_ONE")

	res, err := n.run(g, `{}`, []string{})
	if err == nil {
		t.Fatalf("a gear asking for a name nothing supplies ran anyway: %q", res.Stdout)
	}
	if !errors.Is(err, secrets.ErrUnresolved) {
		t.Errorf("the refusal is not an unresolved-name refusal, so a caller cannot tell it apart: %v", err)
	}
	if !strings.Contains(err.Error(), "MISSING_ONE") {
		t.Errorf("the refusal does not name the name, which is the whole of its usefulness: %v", err)
	}
	if !strings.Contains(err.Error(), g.Name) {
		t.Errorf("the refusal does not say which gear asked: %v", err)
	}
	// PRESENT_ONE resolved. Naming it too would send the operator to fix
	// something that is not broken.
	if strings.Contains(err.Error(), "PRESENT_ONE") {
		t.Errorf("the refusal names a name that IS supplied: %v", err)
	}

	if res.Stdout != "" || res.Produced != nil {
		t.Errorf("the gear ran: stdout %q, produced %+v", res.Stdout, res.Produced)
	}
	runs, err := n.gears.ListRuns(context.Background(), g.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("a run that never happened is in the operator's audit trail: %+v", runs)
	}
	if hits := find(t, n.root, "ran.txt"); len(hits) > 0 {
		t.Errorf("the gear left a file behind, so it executed: %v", hits)
	}
	// Nothing was even written to disk for it: the refusal happens before the
	// gear's source is materialised, which is what "does not run at all" means.
	if left, _ := filepath.Glob(filepath.Join(n.dataDir, "gears", g.Name, "v*")); len(left) > 0 {
		t.Errorf("the gear's source was materialised for a run that was refused: %v", left)
	}
}

// 4. THE test. A secret is in the gear's environment and in nothing else the
// server produces.
//
// It is written as a search rather than as a field-by-field assertion on
// purpose: a new surface that starts carrying a value fails here, and a caller
// that forgot to redact fails here, without anyone having remembered to add a
// line. The positive control comes first and is not optional — the gear dumps
// its own environment into the workspace, and if the value is not THERE then
// every absence below is an absence of nothing.
func TestASecretIsInTheEnvironmentAndNowhereElse(t *testing.T) {
	read := logs(t)
	n := newNamed(t)

	n.global("LEDGER_TOKEN", secrets.KindSecret, storedSecret)
	n.mountSecret("MOUNTED_TOKEN", mountedSecret)

	// The gear publishes its credential every way a gear can: on stdout, on
	// stderr, in the name of a file it produces, and in the name of one that is
	// refused. Nothing here writes the value into the gear's own source.
	g := n.naming("blabber", "set -u\n"+
		"env > out/environment\n"+
		"printf 'stdout carries %s\\n' \"$LEDGER_TOKEN\"\n"+
		"printf 'stderr carries %s\\n' \"$MOUNTED_TOKEN\" >&2\n"+
		"printf 'x' > \"out/$LEDGER_TOKEN.txt\"\n"+
		"ln -s /etc/hosts \"out/$MOUNTED_TOKEN.link\" 2>/dev/null || true\n",
		"LEDGER_TOKEN", "MOUNTED_TOKEN")

	res, err := n.run(g, `{}`, []string{})
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}

	// The positive control. Both values were really handed to the process, from
	// both sources, so everything below is asserting an absence that means
	// something.
	env := n.environment(res)
	if env["LEDGER_TOKEN"] != storedSecret {
		t.Fatalf("the gear was given LEDGER_TOKEN=%q, so this test would prove nothing", env["LEDGER_TOKEN"])
	}
	if env["MOUNTED_TOKEN"] != mountedSecret {
		t.Fatalf("the gear was given MOUNTED_TOKEN=%q, so this test would prove nothing", env["MOUNTED_TOKEN"])
	}

	// Something was removed. Without this the searches below would also pass on
	// a gear that printed nothing at all.
	if !strings.Contains(res.Stdout, secrets.Placeholder) || !strings.Contains(res.Stderr, secrets.Placeholder) {
		t.Errorf("no placeholder in the output: either the gear printed nothing, or nothing redacted it\nstdout %q\nstderr %q",
			res.Stdout, res.Stderr)
	}

	// Everything the run handed back.
	absent(t, "the result's stdout", res.Stdout, storedSecret, mountedSecret)
	absent(t, "the result's stderr", res.Stderr, storedSecret, mountedSecret)
	for _, p := range res.Produced {
		absent(t, "a produced file's name", p.Name, storedSecret, mountedSecret)
		absent(t, "a produced file's path", p.Path, storedSecret, mountedSecret)
	}
	absent(t, "the report of what was not collected", strings.Join(res.Ignored, "\n"), storedSecret, mountedSecret)

	// The recorded run, read back the way the API reads it.
	runs, err := n.gears.ListRuns(context.Background(), g.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("the run was not recorded, so there is nothing here to search: %+v", runs)
	}
	absent(t, "the recorded run's stdout", runs[0].Stdout, storedSecret, mountedSecret)
	absent(t, "the recorded run's stderr", runs[0].Stderr, storedSecret, mountedSecret)
	absent(t, "the recorded run's arguments", runs[0].Args, storedSecret, mountedSecret)

	// The row itself, not the struct: a column nobody reads back is still a
	// column somebody can SELECT.
	var stdout, stderr, args string
	if err := n.db.QueryRow(`SELECT stdout, stderr, args FROM gear_runs WHERE gear_id = ?`, g.ID).
		Scan(&stdout, &stderr, &args); err != nil {
		t.Fatalf("read the recorded run: %v", err)
	}
	absent(t, "the gear_runs row", stdout+"\n"+stderr+"\n"+args, storedSecret, mountedSecret)

	// What the server said about the run.
	absent(t, "the server's log", read(), storedSecret, mountedSecret)

	// The database on disk. The stored secret is encrypted at rest, so its
	// plaintext must not be in the file at all — this is where an encryption
	// that quietly stopped happening surfaces.
	absent(t, "the database file", dbBytes(t, n.dataDir), storedSecret, mountedSecret)

	// And the live stream a watching operator sees, which is the one surface
	// that gets the output BEFORE the redacted record exists.
	var mu sync.Mutex
	var streamed strings.Builder
	streamRes, err := n.exec.RunStream(context.Background(), g, `{}`,
		Caller{AgentID: &n.agentID, WorkspaceID: &n.wsID},
		func(stream, chunk string) {
			mu.Lock()
			defer mu.Unlock()
			streamed.WriteString(chunk)
		})
	if err != nil {
		t.Fatalf("streamed run: %v (stderr: %s)", err, streamRes.Stderr)
	}
	mu.Lock()
	watched := streamed.String()
	mu.Unlock()
	if !strings.Contains(watched, secrets.Placeholder) {
		t.Errorf("no placeholder on the operator's stream: either it carried nothing, or nothing redacted it: %q", watched)
	}
	absent(t, "the operator's live stream", watched, storedSecret, mountedSecret)
}

// 4, continued. A secret split across two chunks of live output is still
// redacted.
//
// This is the subtlest half of the guarantee and the easiest to lose: replacing
// the carrying Stream with a plain per-chunk replacement passes every other
// test in this file, and publishes the first half of a credential to whoever is
// watching. So the gear writes its token in two pieces with a pause between
// them, which is two writes into the pipe and — with a reader draining it — two
// chunks.
func TestASecretSplitAcrossTwoChunksIsStillRedactedOnTheLiveStream(t *testing.T) {
	n := newNamed(t)
	n.global("LEDGER_TOKEN", secrets.KindSecret, storedSecret)

	// The padding is what makes the first chunk large enough for the tap to
	// release something; without it the whole output is held back and the test
	// would pass on a stream that never split at all.
	g := n.naming("stutterer", "set -u\n"+
		"printf 'A%.0s' {1..200}\n"+
		"printf '%s' \"${LEDGER_TOKEN:0:12}\"\n"+
		"sleep 0.3\n"+
		"printf '%s\\n' \"${LEDGER_TOKEN:12}\"\n",
		"LEDGER_TOKEN")

	var mu sync.Mutex
	var chunks []string
	res, err := n.exec.RunStream(context.Background(), g, `{}`,
		Caller{AgentID: &n.agentID, WorkspaceID: &n.wsID},
		func(stream, chunk string) {
			mu.Lock()
			defer mu.Unlock()
			chunks = append(chunks, chunk)
		})
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}
	mu.Lock()
	got := chunks
	mu.Unlock()

	if len(got) < 2 {
		t.Fatalf("the whole output arrived in %d chunk(s), so nothing was split and this test proved nothing: %q", len(got), got)
	}
	joined := strings.Join(got, "")
	if !strings.Contains(joined, secrets.Placeholder) {
		t.Errorf("no placeholder on the stream: either the token never got there, or nothing redacted it: %q", joined)
	}
	absent(t, "the operator's live stream", joined, storedSecret)
	// The first piece on its own is what a per-chunk redactor would emit, and
	// twelve characters of a credential is a leak.
	absent(t, "the operator's live stream", joined, storedSecret[:12])
}

// 5. A variable's value is shown exactly where a secret's is not, so the two
// are genuinely different rather than one mechanism with a label on it.
//
// One gear, one run, both names, and the same three surfaces asked about each.
func TestAVariablesValueIsShownWhereASecretsIsNot(t *testing.T) {
	n := newNamed(t)
	n.global("LEDGER_ENDPOINT", secrets.KindVariable, plainVariable)
	n.global("LEDGER_TOKEN", secrets.KindSecret, storedSecret)

	g := n.naming("teller", "set -u\n"+
		"env > out/environment\n"+
		"printf 'endpoint %s token %s\\n' \"$LEDGER_ENDPOINT\" \"$LEDGER_TOKEN\"\n",
		"LEDGER_ENDPOINT", "LEDGER_TOKEN")

	res, err := n.run(g, `{}`, []string{})
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}

	// Both reached the process. The difference is entirely in what is published
	// afterwards, and that is the claim.
	env := n.environment(res)
	if env["LEDGER_ENDPOINT"] != plainVariable || env["LEDGER_TOKEN"] != storedSecret {
		t.Fatalf("the gear was given endpoint=%q token=%q; both must arrive for the comparison to mean anything",
			env["LEDGER_ENDPOINT"], env["LEDGER_TOKEN"])
	}

	if !strings.Contains(res.Stdout, plainVariable) {
		t.Errorf("a variable's value was withheld from the gear's own output, and a variable is the kind that is shown: %q", res.Stdout)
	}
	absent(t, "the gear's output", res.Stdout, storedSecret)

	runs, err := n.gears.ListRuns(context.Background(), g.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("the run was not recorded: %+v", runs)
	}
	if !strings.Contains(runs[0].Stdout, plainVariable) {
		t.Errorf("the recorded run withheld a variable's value: %q", runs[0].Stdout)
	}
	absent(t, "the recorded run", runs[0].Stdout, storedSecret)

	// And in the list the interface reads, which is where an operator goes to
	// check what a name means.
	records, err := n.store.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]secrets.Record{}
	for _, rec := range records {
		byName[rec.Name] = rec
	}
	if got := byName["LEDGER_ENDPOINT"]; got.Value != plainVariable {
		t.Errorf("the interface is shown %q for a variable; it is the kind whose value is shown", got.Value)
	}
	if got := byName["LEDGER_TOKEN"]; got.Value != "" {
		t.Errorf("the interface is shown %q for a secret; there is nowhere in that response for a value to go", got.Value)
	}
	if got := byName["LEDGER_TOKEN"]; got.Kind != secrets.KindSecret {
		t.Errorf("a secret describes itself as %q to the interface", got.Kind)
	}
}

// The sticky-secret rule, which is the judgement call this phase was asked to
// justify: a workspace override changes a name's VALUE normally, and cannot
// turn a name that is a secret install-wide back into something that gets
// printed.
//
// It is worth its own test because the accident it prevents is silent and
// permanent — setting a workspace override for a production key is exactly the
// moment somebody picks the wrong radio button, and the value would then be in
// a chat transcript with nothing to say it had happened.
func TestAWorkspaceOverrideCannotUnhideASecret(t *testing.T) {
	n := newNamed(t)
	n.global("ROTATING_TOKEN", secrets.KindSecret, storedSecret)
	// The wrong radio button: the same name, overridden here as a variable.
	n.forWorkspace("ROTATING_TOKEN", secrets.KindVariable, mountedSecret)

	g := n.naming("rotator", "set -u\nenv > out/environment\nprintf 'token %s\\n' \"$ROTATING_TOKEN\"\n",
		"ROTATING_TOKEN")
	env, res := n.given(g)

	// The override won the value, which is what an override is for.
	if env["ROTATING_TOKEN"] != mountedSecret {
		t.Fatalf("the workspace override did not win the value: the gear was given %q", env["ROTATING_TOKEN"])
	}
	// And not the kind.
	absent(t, "the gear's output", res.Stdout, mountedSecret)
	if !strings.Contains(res.Stdout, secrets.Placeholder) {
		t.Errorf("the overridden value was printed as itself: %q", res.Stdout)
	}

	statuses, err := n.exec.env.Describe(context.Background(), &n.wsID, []string{"ROTATING_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Kind != secrets.KindSecret {
		t.Errorf("the approval screen says %+v; it must say the word the run will act on", statuses)
	}
}

// A dry run deliberately bypasses the approval gate — that is what it is for.
// It must not also bypass the credentials.
//
// Any signed-in member can author a gear (POST /api/v1/gears takes no
// administrator) and dry-run it (POST /api/v1/gears/{id}/run gates only the
// network half). If a dry run resolves declared names, then reading this
// install's secrets takes one request carrying six lines of bash, and the
// redactor does not help: the value can be printed back with any transformation
// at all, and a bash parameter substitution is enough.
//
// The plan's stated control is that the operator reads the source before
// approving. Nothing here has been approved.
func TestADryRunOfUnapprovedCodeIsNotGivenTheInstallsSecrets(t *testing.T) {
	n := newNamed(t)
	n.global("PAYROLL_TOKEN", secrets.KindSecret, storedSecret)

	// Forged and NOT approved, which is the state a dry run exists for. The
	// gear prints its credential with the hyphens swapped for underscores: the
	// redactor searches for the value it injected, so any transformation at all
	// walks straight past it.
	ctx := context.Background()
	g, err := n.gears.Forge(ctx, "curious", "unreviewed code", nil, "bash", "main.sh", "",
		[]string{"PAYROLL_TOKEN"},
		[]File{{Path: "main.sh", Content: "printf 'obscured=%s\\n' \"${PAYROLL_TOKEN//-/_}\"\nprintf 'length=%s\\n' \"${#PAYROLL_TOKEN}\"\n"}},
		n.wsID, n.agentID)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if g.Status != StatusPending {
		t.Fatalf("the gear is %q; this test is about code nobody has approved", g.Status)
	}

	// Exactly the caller the dry-run handler builds.
	res, err := n.exec.Run(ctx, g, `{}`, Caller{DryRun: true})
	if err != nil {
		// Refused. The install's credentials stayed where they were, which is
		// the property under test — whichever way it is achieved.
		t.Logf("the dry run was refused, which is one correct answer: %v", err)
		return
	}

	obscured := strings.ReplaceAll(storedSecret, "-", "_")
	if strings.Contains(res.Stdout, obscured) || !strings.Contains(res.Stdout, "length=0") {
		t.Errorf("a dry run of code nobody approved was handed this install's secret, and printed it straight "+
			"back past the redactor:\n\t%s\napproval is the control and nothing here was approved — any signed-in "+
			"member can author that gear and run it", strings.ReplaceAll(strings.TrimSpace(res.Stdout), "\n", "\n\t"))
	}
}
