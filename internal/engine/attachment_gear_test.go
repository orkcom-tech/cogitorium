package engine

// The other half of an attachment: the file a model was refused is still a file
// the agent can work on, and the way it works on one is a gear.
//
// This is the claim the refusals make — "the file is in the workspace: give its
// path to a gear" — and a refusal that says so while the path does not in fact
// work is worse than no refusal at all, because the agent will try it and be
// told no twice. So the gear here is a real gear, forged, approved, and run by
// the subprocess backend that an install without Docker actually uses. What it
// received is read back off the disk and compared to what was attached.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// Six files: two a model can be shown, three no model can look inside, and one
// that is text without being called text. The point of the spread is that ONE
// answer covers all of them — every one of them is a file in the workspace at a
// path — and the model's ability to read it changes nothing about that.
var attachedSpread = []struct {
	name  string
	data  []byte
	shown bool // whether a model with every modality declared would be shown it
}{
	{"notes.md", []byte("# what we agreed\n- the spreadsheet is authoritative\n"), true},
	{"diagram.png", pngOnePixelBytes, true},
	{"report.pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n"), true},
	{"books.zip", append([]byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"), []byte{0x00, 0xff, 0x7f, 0x00, 0xff, 0x7f}...), false},
	{"q3.xlsx", append([]byte("PK\x03\x04\x14\x00\x06\x00\x08\x00"), []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x11}...), false},
	{"standup.mp4", append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), []byte{0x00, 0x01, 0x02, 0x03}...), false},
}

// pngOnePixelBytes is a complete 68-byte PNG of one transparent pixel.
var pngOnePixelBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// TestEveryAttachedFileReachesAGearByItsPath is the gear route, end to end, for
// every type at once: the files are classified exactly as a message would
// classify them, and then all six — the shown and the refused alike — are
// handed to one gear by the same paths the note gave the model.
func TestEveryAttachedFileReachesAGearByItsPath(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable; the subprocess backend has nothing to run")
	}
	ctx := context.Background()
	f := newFilesFixture(t)

	// Where the upload handler lands a file: one directory per arrival, the
	// operator's own filename kept inside it.
	const drop = workdir.AttachmentDir + "/20260807-120000.000-a1b2c3d4/"
	var paths []string
	for _, file := range attachedSpread {
		f.put(drop+file.name, file.data)
		paths = append(paths, drop+file.name)
	}

	// First, what the message itself would do with them. This is here rather
	// than in a test of its own because the whole claim below is about the
	// files the model was REFUSED, and a spread where nothing was refused would
	// prove the opposite of what it looks like.
	atts, parts, err := f.e.collectAttachments(f.wsID, paths)
	if err != nil {
		t.Fatalf("attaching six ordinary files failed the message: %v", err)
	}
	shown := map[string]bool{}
	for _, a := range atts {
		shown[filepath.Base(a.Path)] = a.ToModel()
	}
	for _, file := range attachedSpread {
		if got := shown[file.name]; got != file.shown {
			t.Errorf("%q was %s to the model; this test's premise is the opposite", file.name, map[bool]string{true: "shown", false: "not shown"}[got])
		}
	}
	if len(parts) != 3 {
		t.Fatalf("%d of the six files went to the model, want 3 — the spread no longer covers both routes", len(parts))
	}
	for _, a := range atts {
		if a.ToModel() {
			continue
		}
		// The refusal is what sends the agent here. If it stops naming the
		// route, the test below proves a path nobody is told to take.
		if !strings.Contains(a.Skipped, "gear") {
			t.Errorf("%q was refused without naming the gear route: %q", a.Path, a.Skipped)
		}
	}

	// A gear that opens everything it is given and copies it back out. Copying
	// rather than counting is deliberate: a byte count would pass on a file
	// that was truncated and re-padded, and what has to be true is that the
	// gear opened the operator's file and not a description of it.
	g, err := f.gears.Forge(ctx, "carry", "copies every file it is given", nil, "bash", "main.sh", "",
		[]gear.File{{Path: "main.sh", Content: "set -eu\nn=0\nfor p in $(find in -type f | sort); do\n  cp \"$p\" \"out/$(basename \"$p\")\"\n  n=$((n+1))\ndone\necho \"$n\"\n"}},
		f.wsID, f.orch.ID)
	if err != nil {
		t.Fatalf("forge the gear: %v", err)
	}
	if _, err := f.gears.SetStatus(ctx, g.ID, gear.StatusApproved); err != nil {
		t.Fatalf("approve the gear: %v", err)
	}

	args, err := json.Marshal(map[string]any{gearFilesArg: paths})
	if err != nil {
		t.Fatalf("build the call: %v", err)
	}
	out, err := f.call(gearToolPrefix+"carry", string(args))
	if err != nil {
		t.Fatalf("handing the attachments to a gear: %v", err)
	}

	var result struct {
		Output   string `json:"output"`
		NotTaken []any  `json:"not_taken"`
		Files    []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("the gear's result is not the JSON the model reads: %v (%s)", err, out)
	}
	if strings.TrimSpace(result.Output) != fmt.Sprint(len(attachedSpread)) {
		t.Fatalf("the gear opened %q of the %d files it was given", strings.TrimSpace(result.Output), len(attachedSpread))
	}
	if len(result.NotTaken) > 0 {
		t.Errorf("the gear's output was not all collected: %v", result.NotTaken)
	}

	// What came back out, compared with what went in. Every file, including the
	// three the model was never shown — those are the ones this route exists
	// for.
	landed := map[string]string{}
	for _, p := range result.Files {
		landed[p.Name] = p.Path
	}
	for _, file := range attachedSpread {
		rel, ok := landed[file.name]
		if !ok {
			t.Errorf("the gear was never given %q, so the route the refusal names does not work: it produced %v", file.name, landed)
			continue
		}
		got, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("the path the model was handed for %q does not exist: %v", file.name, err)
			continue
		}
		if string(got) != string(file.data) {
			t.Errorf("%q reached the gear as %d bytes and the attachment is %d — a gear given a mangled file is worse than a gear given none",
				file.name, len(got), len(file.data))
		}
	}
}

// The other two ways an agent reaches a workspace path, on the same spread: the
// listing shows every attachment whatever its type, and reading one is answered
// either with the file or with the route that opens it — never with silence.
func TestTheAgentCanListAndOpenEveryAttachment(t *testing.T) {
	f := newFilesFixture(t)

	const drop = workdir.AttachmentDir + "/20260807-120000.000-a1b2c3d4/"
	for _, file := range attachedSpread {
		f.put(drop+file.name, file.data)
	}

	listing, err := f.call("list_files", `{"path":"`+workdir.AttachmentDir+`"}`)
	if err != nil {
		t.Fatalf("list the workspace: %v", err)
	}
	for _, file := range attachedSpread {
		if !strings.Contains(listing, drop+file.name) {
			t.Errorf("%q is attached and invisible to the agent:\n%s", file.name, listing)
		}
	}

	text, err := f.call("read_file", `{"path":"`+drop+`notes.md"}`)
	if err != nil {
		t.Fatalf("read an attached text file: %v", err)
	}
	if !strings.Contains(text, "the spreadsheet is authoritative") {
		t.Errorf("the attached text file came back as %q", text)
	}

	// A binary file is refused BY read_file — the refusal is the routing, and
	// it has to name the argument that works, because that is the agent's only
	// clue that there is another way in.
	if _, err := f.call("read_file", `{"path":"`+drop+`books.zip"}`); err == nil {
		t.Error("a zip was read into the conversation as text")
	} else if !strings.Contains(err.Error(), gearFilesArg) {
		t.Errorf("the agent is refused the zip without being told how to open it: %v", err)
	}
}

// An attachment nobody can read is still an attachment: it is recorded, the
// model is told its path, and the note says plainly that it was not shown.
// Without the last part a model answers as though it had seen the file.
func TestTheNoteTellsTheModelWhatItWasNotShown(t *testing.T) {
	f := newFilesFixture(t)

	const drop = workdir.AttachmentDir + "/20260807-120000.000-a1b2c3d4/"
	for _, file := range attachedSpread {
		f.put(drop+file.name, file.data)
	}
	var paths []string
	for _, file := range attachedSpread {
		paths = append(paths, drop+file.name)
	}

	atts, _, err := f.e.collectAttachments(f.wsID, paths)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	note := attachmentNote(atts)
	for _, file := range attachedSpread {
		if !strings.Contains(note, drop+file.name) {
			t.Errorf("the note never gives the path of %q, which is the only thing the agent could act on:\n%s", file.name, note)
		}
	}
	if !strings.Contains(note, "not shown to you") {
		t.Errorf("the note does not say that three of the six were withheld:\n%s", note)
	}
	if !strings.Contains(note, gearFilesArg) {
		t.Errorf("the note does not name the argument that opens them:\n%s", note)
	}
	// And the kinds it does claim are the kinds llm would actually send.
	for _, a := range atts {
		if a.Kind != "" && a.Kind != llm.PartText && a.Kind != llm.PartImage && a.Kind != llm.PartDocument {
			t.Errorf("%q was recorded as kind %q, which is not something a model can be sent", a.Path, a.Kind)
		}
	}
}
