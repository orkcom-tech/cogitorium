//go:build !windows

package bundle

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// Everything here drives a real contextd stand-in: a /bin/sh program that
// keeps a real space of real files on disk. It is deliberately obedient — it
// writes wherever it is told, exactly as given — because that is what makes
// the path tests evidence. A stand-in that sanitised its own arguments would
// hide the very failure these tests exist to catch: if the bundle package
// stopped refusing "../..", the file would appear outside the space here, and
// the test would see it.
//
// The tag is because the stand-in is a shell script. Windows is
// cross-compiled only.

// space is a contextd stand-in and the directory tree it writes into.
type space struct {
	t   *testing.T
	bin string
	// root is the space itself. base is the tree it sits in, nested deep
	// enough that a path climbing three levels out of the space still lands
	// inside a directory this test can inspect — and not in the machine's
	// temp root, which is where a leaked write would otherwise go.
	root string
	base string
}

func newSpace(t *testing.T) *space {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh to build the contextd stand-in: %v", err)
	}
	// Resolved absolutely so the stand-in does not depend on the PATH the
	// test process happens to have.
	tools := map[string]string{}
	for _, name := range []string{"cat", "find", "mkdir", "awk"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("no %s to build the contextd stand-in: %v", name, err)
		}
		tools[name] = p
	}

	base := t.TempDir()
	s := &space{
		t:    t,
		base: base,
		root: filepath.Join(base, "one", "two", "three", "four", "space"),
		bin:  filepath.Join(base, "contextd"),
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatalf("create space: %v", err)
	}

	script := `#!/bin/sh
root=` + shellQuote(s.root) + `
cat=` + shellQuote(tools["cat"]) + `
find=` + shellQuote(tools["find"]) + `
mkdir=` + shellQuote(tools["mkdir"]) + `
awk=` + shellQuote(tools["awk"]) + `
"$mkdir" -p "$root"
case "$1" in
version)
	printf 'contextd 0.0.0-stand-in\n'
	;;
status)
	printf '{"space_root":"%s","exists":true,"mode":"solo"}\n' "$root"
	;;
file)
	case "$2" in
	list)
		cd "$root" || exit 1
		"$find" . -type f | "$awk" '
			BEGIN { printf "[" }
			{ sub(/^\.\//, ""); if (n++) printf ","; printf "{\"path\":\"%s\",\"version\":\"1\"}", $0 }
			END { printf "]\n" }'
		;;
	get)
		if [ ! -f "$root/$3" ]; then
			printf 'error: not found: %s\n' "$3" >&2
			exit 1
		fi
		"$cat" "$root/$3"
		;;
	put)
		case "$3" in
		*/*) "$mkdir" -p "$root/${3%/*}" || exit 1 ;;
		esac
		"$cat" > "$root/$3"
		;;
	*)
		printf 'error: unknown file subcommand: %s\n' "$2" >&2
		exit 2
		;;
	esac
	;;
*)
	printf 'error: unknown command: %s\n' "$1" >&2
	exit 2
	;;
esac
`
	if err := os.WriteFile(s.bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write contextd stand-in: %v", err)
	}
	return s
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// files lists every file in the whole tree the stand-in can reach, relative
// to it. Comparing this before and after a refused import is what proves the
// refusal wrote nothing — anywhere, not only inside the space.
func (s *space) files() []string {
	s.t.Helper()
	var out []string
	err := filepath.WalkDir(s.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || p == s.bin {
			return nil
		}
		rel, err := filepath.Rel(s.base, p)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		s.t.Fatalf("walk the context tree: %v", err)
	}
	slices.Sort(out)
	return out
}

func (s *space) read(path string) string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(path)))
	if err != nil {
		s.t.Fatalf("read context document %q: %v", path, err)
	}
	return string(raw)
}

// hostilePaths are the paths a bundle must never be able to write to. Each
// one is a way of naming something the workspace being created does not own:
// another workspace's memory, the shared instruction library, or a file
// outside the context space entirely.
var hostilePaths = map[string]string{
	"climbs out of the space":         "../../../etc/passwd",
	"absolute":                        "/etc/passwd",
	"climbs out after one descent":    "a/../../b",
	"the parent itself":               "..",
	"empty":                           "",
	"a null byte":                     "notes\x00.md",
	"fine until it is cleaned":        "shared/../notes.md",
	"a windows-style climb":           `..\..\notes.md`,
	"read as a flag by the store":     "-rf",
	"nothing but a dot":               ".",
	"climbs out at the very end":      "shared/notes/../../../../escaped.md",
	"a deep climb inside a long path": "a/b/c/../../../../../escaped.md",
}

// INVARIANT 6 — the function that places a bundle path under a workspace
// branch, tested on its own. It is exported precisely so this can be direct:
// it is the last gate before a write, and a gate is worth testing without
// anything else in the way.
func TestContextPathRefusesHostilePaths(t *testing.T) {
	t.Parallel()
	const branch = "workspaces/atlas-1"

	for name, p := range hostilePaths {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ContextPath(branch, p)
			if err == nil {
				t.Fatalf("ContextPath(%q, %q) = %q with no error; a bundle must not be able to name where its documents land", branch, p, got)
			}
			if got != "" {
				t.Errorf("ContextPath(%q, %q) refused but still returned %q", branch, p, got)
			}
		})
	}
}

func TestContextPathRootsUnderTheNewBranch(t *testing.T) {
	t.Parallel()
	const branch = "workspaces/atlas-7"
	cases := map[string]string{
		"shared/notes.md":          "workspaces/atlas-7/shared/notes.md",
		"agents/researcher-3/x.md": "workspaces/atlas-7/agents/researcher-3/x.md",
		"notes.md":                 "workspaces/atlas-7/notes.md",
		"./notes.md":               "workspaces/atlas-7/notes.md",
	}
	for rel, want := range cases {
		got, err := ContextPath(branch, rel)
		if err != nil {
			t.Errorf("ContextPath(%q, %q): %v", branch, rel, err)
			continue
		}
		if got != want {
			t.Errorf("ContextPath(%q, %q) = %q, want %q", branch, rel, got, want)
		}
	}

	// A workspace with no branch has nowhere to put a document, and joining
	// onto an empty branch would put it at the root of the whole space.
	if _, err := ContextPath("", "notes.md"); err == nil {
		t.Errorf("ContextPath with no branch was allowed; that writes to the root of the context space")
	}
}

// INVARIANT 6 — the same paths through Import, against a real space. The
// refusal has to happen before anything exists: no file, and no workspace
// either, since a bundle refused halfway leaves the operator to work out
// which half.
func TestImportRefusesHostileContextPathsAndWritesNothing(t *testing.T) {
	for name, hostile := range hostilePaths {
		t.Run(name, func(t *testing.T) {
			sp := newSpace(t)
			i := newInstallWith(t, sp.bin)
			before := sp.files()
			wsBefore := i.workspaceCount()

			b := Bundle{
				Format:    Format,
				Workspace: Workspace{Name: "smuggler"},
				Agents:    []Agent{{Name: workspace.OrchestratorName, IsOrchestrator: true}},
				Context:   []ContextFile{{Path: hostile, Content: "pwned"}},
			}

			_, err := Import(context.Background(), i.stores, b, ImportOptions{OwnerID: i.owner, IncludeContext: true})
			if err == nil {
				t.Fatalf("import accepted the context path %q", hostile)
			}
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("import returned %v, which is not ErrMalformed, so the API would blame itself for the operator's document", err)
			}

			if after := sp.files(); !slices.Equal(before, after) {
				t.Errorf("a refused import wrote to the context space: before %v, after %v", before, after)
			}
			if got := i.workspaceCount(); got != wsBefore {
				t.Errorf("%d workspaces exist after a refused import, want the %d there were: a document refused for one field must create nothing",
					got, wsBefore)
			}
		})
	}
}

// A hostile path is refused even when the caller did not ask for context at
// all, because it is refused as part of reading the document rather than as
// part of writing it.
func TestAHostileContextPathIsRefusedEvenWithoutIncludeContext(t *testing.T) {
	sp := newSpace(t)
	i := newInstallWith(t, sp.bin)

	b := Bundle{
		Format:    Format,
		Workspace: Workspace{Name: "smuggler"},
		Agents:    []Agent{{Name: workspace.OrchestratorName, IsOrchestrator: true}},
		Context:   []ContextFile{{Path: "../../../etc/passwd", Content: "pwned"}},
	}
	if _, err := Import(context.Background(), i.stores, b, ImportOptions{OwnerID: i.owner}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("import returned %v, want ErrMalformed", err)
	}
	if got := i.workspaceCount(); got != 0 {
		t.Errorf("%d workspaces were created from a document with a hostile path, want none", got)
	}
}

// INVARIANT 6 — the honest half: documents that are relative land under the
// new workspace's own branch and nowhere else.
func TestImportedContextLandsUnderTheNewWorkspaceBranch(t *testing.T) {
	sp := newSpace(t)
	i := newInstallWith(t, sp.bin)

	b := Bundle{
		Format:    Format,
		Workspace: Workspace{Name: "atlas"},
		Agents:    []Agent{{Name: workspace.OrchestratorName, IsOrchestrator: true}},
		Context: []ContextFile{
			{Path: "shared/notes.md", Content: "shared memory"},
			{Path: "agents/researcher-91/private.md", Content: "private memory"},
		},
	}

	res := i.mustImport(b, ImportOptions{IncludeContext: true})
	if res.ContextFiles != 2 {
		t.Fatalf("import reports %d context documents, want 2", res.ContextFiles)
	}
	if res.Workspace.Branch == "" {
		t.Fatalf("the imported workspace has no branch, so its documents have no home")
	}

	for rel, want := range map[string]string{
		"shared/notes.md":                 "shared memory",
		"agents/researcher-91/private.md": "private memory",
	} {
		if got := sp.read(res.Workspace.Branch + "/" + rel); got != want {
			t.Errorf("%s/%s reads %q, want %q", res.Workspace.Branch, rel, got, want)
		}
	}
	for _, f := range sp.files() {
		if !strings.HasPrefix(filepath.ToSlash(f), "one/two/three/four/space/"+res.Workspace.Branch+"/") {
			t.Errorf("the import wrote %q, which is outside the branch %q it created", f, res.Workspace.Branch)
		}
	}
}

// The round trip: everything a workspace is, out of one install and into
// another, with the copy checked by shape rather than by id.
func TestRoundTripIntoAnotherInstall(t *testing.T) {
	srcSpace := newSpace(t)
	dstSpace := newSpace(t)
	src := newInstallWith(t, srcSpace.bin)
	dst := newInstallWith(t, dstSpace.bin)

	ws := src.seedAtlas("atlas")
	if err := src.stores.Context.Put(context.Background(), ws.SharedBranch()+"/notes.md", "the atlas brief"); err != nil {
		t.Fatalf("seed a context document: %v", err)
	}

	// The destination knows one of the two models, so the round trip covers
	// both halves of the model rule at once.
	dst.model(dst.provider("anthropic-of-this-install", llm.TypeAnthropic, ""), sonnet)

	b := src.export(ws.ID, Options{Gears: true, Context: true})
	if len(b.Context) != 1 || b.Context[0].Path != "shared/notes.md" {
		t.Fatalf("exported context = %v, want one document at the branch-relative path %q", b.Context, "shared/notes.md")
	}

	res := dst.mustImport(b, ImportOptions{Name: "atlas (imported)", IncludeGears: true, IncludeContext: true})
	copyWS := res.Workspace

	if res.Agents != 2 || res.Wires != 1 || res.ContextFiles != 1 {
		t.Errorf("report says %d agents, %d wires, %d context documents; want 2, 1, 1", res.Agents, res.Wires, res.ContextFiles)
	}

	// Agents, by name.
	orch := dst.agentByName(copyWS.ID, workspace.OrchestratorName)
	researcher := dst.agentByName(copyWS.ID, "researcher")
	if !orch.IsOrchestrator || researcher.IsOrchestrator {
		t.Errorf("the copy has the wrong orchestrator: %q=%v, %q=%v",
			orch.Name, orch.IsOrchestrator, researcher.Name, researcher.IsOrchestrator)
	}
	if researcher.Role != "You find sources." {
		t.Errorf("the researcher's role came across as %q", researcher.Role)
	}
	if researcher.Avoid != "Never cite a paper you did not read." {
		t.Errorf("the researcher's prohibitions came across as %q", researcher.Avoid)
	}
	if researcher.ModelID != nil {
		t.Errorf("the researcher is bound to model %d, but this install has no %q", *researcher.ModelID, opus)
	}
	if orch.ModelID == nil {
		t.Errorf("the orchestrator lost its model, though this install has %q", sonnet)
	}

	// The wire, between the same two names.
	wires := dst.wires(copyWS.ID)
	if len(wires) != 1 || wires[0].FromAgentID != orch.ID || wires[0].ToAgentID != researcher.ID || wires[0].Label != "delegates" {
		t.Errorf("the copy's wires are %v, want one %q wire from %q (%d) to %q (%d)",
			wires, "delegates", orch.Name, orch.ID, researcher.Name, researcher.ID)
	}

	// The gear: forged here, pending, bound to the same agent by name.
	copied := dst.gearByName("wordcount")
	if copied.Status != gear.StatusPending {
		t.Errorf("the imported gear is %q, want %q", copied.Status, gear.StatusPending)
	}
	if copied.Entrypoint != "main.py" {
		t.Errorf("the imported gear's entrypoint is %q, want %q", copied.Entrypoint, "main.py")
	}
	files := dst.gearFiles(copied)
	if len(files) != 1 || files[0].Content != "print(len(input().split()))" {
		t.Errorf("the imported gear's source is %v, want the exported one", files)
	}
	bindings := dst.bindings(copyWS.ID)
	if len(bindings) != 1 || bindings[0].AgentID == nil || *bindings[0].AgentID != researcher.ID {
		t.Errorf("the copy's gear bindings are %v, want wordcount bound to the researcher (%d)", bindings, researcher.ID)
	}

	// The context document, under the copy's own branch — not the original's.
	if copyWS.Branch == ws.Branch {
		t.Fatalf("both workspaces claim the branch %q, so this test cannot tell re-rooting from luck", copyWS.Branch)
	}
	if got := dstSpace.read(copyWS.Branch + "/shared/notes.md"); got != "the atlas brief" {
		t.Errorf("the copy's context document reads %q, want %q", got, "the atlas brief")
	}
	if got := srcSpace.read(ws.SharedBranch() + "/notes.md"); got != "the atlas brief" {
		t.Errorf("the exporting install's own document now reads %q", got)
	}
}

// Asking for context an install cannot reach is refused before anything is
// created, rather than producing a workspace whose documents are half there.
func TestImportRefusesContextWhenContextverseIsUnreachable(t *testing.T) {
	i := newInstall(t) // an install with no contextd at all
	b := Bundle{
		Format:    Format,
		Workspace: Workspace{Name: "atlas"},
		Agents:    []Agent{{Name: workspace.OrchestratorName, IsOrchestrator: true}},
		Context:   []ContextFile{{Path: "shared/notes.md", Content: "hello"}},
	}

	_, err := Import(context.Background(), i.stores, b, ImportOptions{OwnerID: i.owner, IncludeContext: true})
	if !errors.Is(err, contextstore.ErrUnavailable) {
		t.Fatalf("import returned %v, want an unavailable-Contextverse error", err)
	}
	if got := i.workspaceCount(); got != 0 {
		t.Errorf("%d workspaces were created before the context check, want none", got)
	}
}
