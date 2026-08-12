package server

// One question, asked once per kind of file: can it get IN through the
// receiver, reach the agent, be opened by a real gear, and come back OUT — and
// can what came out go back in, which is the only thing that makes a second
// gear possible?
//
// Everything here drives the whole path. The receiver is the real delivery
// handler, the workspace directory is a real directory on disk, the orchestrator
// is the real engine loop, and the gear is a real gear — forged through the
// gear store the forge_gear tool uses, approved through the operator's own HTTP
// route, and executed by the subprocess backend that an install without Docker
// actually runs. The only thing scripted is what the model SAYS, which is the
// one thing that cannot be had from a real one.
//
// Three rules shape every assertion below.
//
// BYTES, NEVER SIZES. A round trip that checks a length passes on a file that
// arrived truncated and was re-padded, or encoded, or replaced with a different
// file of the same size. So every step compares the actual bytes against the
// actual bytes that were posted, and the gear proves it opened the file by
// copying it back out rather than by reporting a number. The one size asserted
// anywhere here is the count in the gear's own file report, held against the
// file that landed — because a number the model reads has to be a measurement
// of something — and it is never what proves the file arrived.
//
// REAL FILES, NOT PLAUSIBLE ONES. The PNG is encoded by image/png, the zip is
// built by archive/zip, and the PDF is assembled object by object with a real
// cross-reference table. A made-up byte string starting with "%PDF" would prove
// nothing about a path whose whole job is carrying a file unchanged.
//
// THE NAME IS NOT THE BYTES. What a caller calls a file decides a path on the
// operator's machine, so it is the one thing here that deliberately does not
// survive intact. Every assertion about a landed name is therefore exact rather
// than a prefix, on both doors, and with the names somebody hostile would send
// as well as the ones an honest caller does.

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// filesArg is the argument the engine adds to every gear's schema to hand it
// workspace files. It is spelled out here because engine.gearFilesArg is
// unexported, and nothing below asserts on the constant itself — what is
// asserted is that a call using this name puts a real file in front of a real
// gear, which no amount of agreeing about a string would prove.
const filesArg = "_files"

// --- The files. Each one is built, not pasted. ----------------------------

// pngBytes encodes a real 3x2 PNG with four distinct pixels. The colours are
// there so that a re-encoded image — one that survived a decode and an encode
// somewhere on the path — would not compare equal to this.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	img.Set(1, 0, color.NRGBA{G: 0xff, A: 0xff})
	img.Set(2, 0, color.NRGBA{B: 0xff, A: 0xff})
	img.Set(0, 1, color.NRGBA{R: 0x12, G: 0x34, B: 0x56, A: 0x78})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode the test PNG: %v", err)
	}
	return buf.Bytes()
}

// pdfBytes assembles a minimal but genuinely valid PDF: five objects, a
// cross-reference table whose offsets are measured rather than guessed, and a
// trailer pointing at it. Measuring the offsets is why this is built here
// instead of held as a literal — a literal with wrong offsets is a file every
// reader rejects, and it would still travel down this path unnoticed.
func pdfBytes(t *testing.T) []byte {
	t.Helper()
	stream := "BT /F1 18 Tf 12 72 Td (round trip) Tj ET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 144 144] /Contents 4 0 R " +
			"/Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	// The binary comment every generator writes on line two: it tells anything
	// transferring the file that it holds 8-bit data and must not be treated as
	// text. It is also what makes these bytes fail a UTF-8 test, which is how
	// this package decides a file is not prose.
	buf.Write([]byte{'%', 0xe2, 0xe3, 0xcf, 0xd3, '\n'})

	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	startxref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, startxref)
	return buf.Bytes()
}

// zipEntry is one member of the archive, kept beside the archive itself so the
// test can compare what the gear handed back against what went in.
type zipEntry struct {
	name string
	body []byte
}

// zipContents is deliberately mixed: text, a nested path, and a binary member.
// Unpacking is only interesting if what comes out is not all the same shape.
func zipContents(t *testing.T) []zipEntry {
	t.Helper()
	return []zipEntry{
		{"readme.txt", []byte("this archive was built by archive/zip\nand unpacked by a gear\n")},
		{"data/rows.csv", []byte("region,units\nnorth,17\nsouth,4\n")},
		{"art/pixel.png", pngBytes(t)},
	}
}

func zipBytes(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := w.Create(e.name)
		if err != nil {
			t.Fatalf("add %q to the test archive: %v", e.name, err)
		}
		if _, err := f.Write(e.body); err != nil {
			t.Fatalf("write %q into the test archive: %v", e.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the test archive: %v", err)
	}
	return buf.Bytes()
}

// gzipBytes is the file with nothing to identify it: no extension, no
// Content-Type header, and bytes that are not text. It is a real gzip stream
// rather than random noise, so what travels is a file somebody could actually
// have sent.
func gzipBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte("nobody said what this is, and it is still a file\n")); err != nil {
		t.Fatalf("write the test gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the test gzip: %v", err)
	}
	return buf.Bytes()
}

// --- The gears. Both are real, and both must actually open the file. -------

type forgedGear struct{ name, runtime, entrypoint, source string }

// carryGear copies every file it is handed straight back out. Copying rather
// than counting is the whole point: a byte count comes back right for a file
// that was truncated and re-padded, and what has to be true is that the gear
// opened the caller's bytes.
//
// It prints the path each file arrived at, with the leading in/ removed, which
// is the workspace path the agent named — so the printed line is evidence the
// file was staged where the protocol says, and not merely that a name was
// passed around.
var carryGear = forgedGear{
	name: "carry", runtime: "bash", entrypoint: "main.sh",
	source: "set -eu\n" +
		"cat >/dev/null\n" + // this gear takes no arguments of its own
		"find in -type f | sort | while IFS= read -r p; do\n" +
		"  printf '%s\\n' \"${p#in/}\"\n" +
		"  cp \"$p\" \"out/$(basename \"$p\")\"\n" +
		"done\n",
}

// unpackGear is the operator's own example: an archive goes in, and what was
// inside it comes back out. Each member is written into out/ under a flattened
// name, so a nested entry lands as a file rather than needing a directory the
// collector would then have to be trusted to walk.
//
// Like carryGear it prints the path it opened, with the leading in/ removed,
// before it prints the archive's members. Without that line this gear reports
// only what was INSIDE the file, which is the same output whether the archive
// was staged at the path the caller named or somewhere else entirely — and
// "somewhere else entirely" is exactly what a flattening staging bug does.
var unpackGear = forgedGear{
	name: "unpack", runtime: "python", entrypoint: "main.py",
	source: `import os, sys, zipfile

sys.stdin.read()

lines = []
for root, _, names in os.walk("in"):
    for n in sorted(names):
        p = os.path.join(root, n)
        lines.append(os.path.relpath(p, "in"))
        with zipfile.ZipFile(p) as z:
            for entry in sorted(z.namelist()):
                if entry.endswith("/"):
                    continue
                lines.append(entry)
                with open(os.path.join("out", entry.replace("/", "__")), "wb") as fh:
                    fh.write(z.read(entry))
print("\n".join(lines))
`,
}

// forge registers a gear the way an agent's forge_gear does — through the real
// store, on behalf of the orchestrator, which self-binds it — and approves it
// the way the operator does, through the HTTP route that is the only thing in
// this install allowed to make agent-authored code runnable.
func (d *door) forge(t *testing.T, g forgedGear) gear.Gear {
	t.Helper()
	ctx := context.Background()
	orch := d.agent(t, workspace.OrchestratorName)

	// A schema with a properties object, because that is what the engine hangs
	// the file argument off: a gear forged with a bare "{}" is never offered
	// _files at all, and this test would then be asserting on an argument the
	// model was never given.
	forged, err := d.srv.gears.Forge(ctx, g.name, "opens the files it is given", nil,
		g.runtime, g.entrypoint, `{"type":"object","properties":{}}`, nil,
		[]gear.File{{Path: g.entrypoint, Content: g.source}}, d.wsID, orch.ID)
	if err != nil {
		t.Fatalf("forge the %q gear: %v", g.name, err)
	}
	rec := d.request(t, http.MethodPatch, "/api/v1/gears/"+id(forged.ID), d.adminTok, `{"status":"approved"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approving the %q gear through the operator's route: %d %s", g.name, rec.Code, rec.Body.String())
	}
	return forged
}

// newRunningDoor is the ordinary door fixture with gears that actually run.
//
// Every other test in this package holds a Docker runner pointed at an image
// that does not exist, which is correct for them because none of them execute a
// gear. Here a gear has to really open a file, so the executor is given no
// sandbox — which is not a weakened fixture but the exact backend an install
// without Docker runs, and the one whose file protocol has the most to get
// wrong.
func newRunningDoor(t *testing.T) *door {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not on this machine, so the subprocess backend has nothing to run a gear with")
	}
	return doorAround(t, newInstallWithSandbox(t, doorListen, nil, nil, nil))
}

// needsPython skips a test whose gear is written in python on a machine that
// has none. The gear is real, so its interpreter has to be too.
func needsPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on this machine, so the unpacking gear has no interpreter")
	}
}

func (d *door) wsRoot() string { return workdir.Dir(d.dir, d.wsID) }

// --- What the round trip is, and how each step is proved. ------------------

// gearAnswer is the tool result the engine gives the model back for a gear call
// that carried files. It is decoded from what the provider actually received,
// not from the engine's own struct.
type gearAnswer struct {
	Output   string         `json:"output"`
	Stderr   string         `json:"stderr"`
	NotTaken []string       `json:"not_taken"`
	Files    []producedFile `json:"files"`
}

// producedFile is one entry of that report: what the gear called the file, the
// workspace path it now has, and how big it is. Path is the promise the whole
// pipeline rests on — gear.Produced calls it "the same form the caller uses to
// hand it to the next gear" — and TestFileRoundTripChainsOneGearIntoTheNext is
// where that sentence is made to be true rather than merely written down.
type producedFile struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// landed is one completed trip, as the checks below read it.
type landed struct {
	path string     // where the file came to rest, relative to the workspace
	body []byte     // what was posted, byte for byte
	res  gearAnswer // what the gear said and what it handed back
}

// kind is one sort of file, and everything the path has to be told about it.
type kind struct {
	name string
	// filename is what the caller calls the file. The inlet case posts a raw
	// body when it is empty, which is a caller naming nothing at all.
	filename string
	// contentType is what the caller says the file is. Empty means the request
	// carries no Content-Type header — not an empty one.
	contentType string
	// accepts is what the file task declares it takes; empty takes anything.
	accepts string
	body    []byte
	gear    forgedGear
	// landsAs is the filename the door gives the file, after the run number it
	// is prefixed with. It is stated rather than derived because the name is
	// the one thing on this path that deliberately does NOT survive: it comes
	// from outside and it decides a path on this machine, so what is kept is a
	// label made of [A-Za-z0-9._-] and everything else becomes an underscore.
	// Spelling out the result is how a reader learns that the bytes are carried
	// unchanged and the name is not.
	landsAs string
	// check is steps 4 and 5: what the gear must have seen, and what must have
	// come back into the workspace.
	check func(t *testing.T, d *door, l landed)
}

func fileKinds(t *testing.T) []kind {
	t.Helper()
	archive := zipContents(t)
	return []kind{
		{
			name: "json", filename: "ticket.json", contentType: "application/json",
			accepts: "application/json",
			body:    []byte("{\n  \"id\": 7,\n  \"title\": \"disk full\",\n  \"tags\": [\"ops\", \"p1\"]\n}\n"),
			landsAs: "ticket.json",
			gear:    carryGear, check: checkCopiedBack,
		},
		{
			name: "csv", filename: "usage.csv", contentType: "text/csv",
			accepts: "text/csv",
			body:    []byte("region,units,notes\nnorth,17,\"comma, inside\"\nsouth,4,\n"),
			landsAs: "usage.csv",
			gear:    carryGear, check: checkCopiedBack,
		},
		{
			name: "png", filename: "pixels.png", contentType: "image/png",
			accepts: "image/png", body: pngBytes(t), landsAs: "pixels.png",
			gear: carryGear, check: checkCopiedBack,
		},
		{
			name: "pdf", filename: "report.pdf", contentType: "application/pdf",
			accepts: "application/pdf", body: pdfBytes(t), landsAs: "report.pdf",
			gear: carryGear, check: checkCopiedBack,
		},
		{
			name: "zip", filename: "books.zip", contentType: "application/zip",
			accepts: "application/zip", body: zipBytes(t, archive), landsAs: "books.zip",
			gear:  unpackGear,
			check: checkUnpacked(archive),
		},
		{
			// No name, no header. The door has nothing to go on but the bytes —
			// so the file is called "payload", and gets no extension rather
			// than an invented one.
			name: "no extension and no content type", filename: "",
			contentType: "", accepts: "", body: gzipBytes(t), landsAs: "payload",
			gear: carryGear, check: checkCopiedBack,
		},
		{
			name:     "a name with spaces and a unicode character",
			filename: "quarterly report – café ☕.txt",
			// The header carries a parameter as well, because a real client
			// sends one and a door that compared the whole string would refuse
			// a file it accepts.
			contentType: "text/plain; charset=utf-8", accepts: "text/plain",
			body: []byte("prepared by café ☕ — 3 pages\nnothing here is ASCII-only\n"),
			// The spaces, the en dash, the é and the emoji are all gone from the
			// NAME — every run of them collapsed to one underscore — while the
			// same characters inside the file are carried through untouched.
			landsAs: "quarterly_report_caf_.txt",
			gear:    carryGear, check: checkCopiedBack,
		},
	}
}

// reportsLine says whether the gear printed exactly this line.
//
// A substring match over the whole of stdout is a weaker claim than it reads
// as: "in/a.txt" is inside "in/a.txt.bak", so a gear handed one file and
// finding a different one NEXT to it would satisfy it. Both gears here print
// one path per line, so a line is the unit to compare, and a staged path that
// grew a suffix or a prefix is a mismatch instead of a match.
func reportsLine(output, want string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimRight(line, "\r") == want {
			return true
		}
	}
	return false
}

// checkReportedSize holds the file report's byte count against the file that is
// actually in the workspace.
//
// It is the one number in the report, and nothing else in this test looked at
// it: setting it to zero in internal/gear left every assertion here green. The
// model reads it to decide whether to hand the file on, split it or say the
// gear produced nothing, and it has no second source for it — so a count that
// is not a measurement of the landed file is a lie no reader can catch.
func checkReportedSize(t *testing.T, f producedFile, onDisk []byte) {
	t.Helper()
	if f.Bytes != int64(len(onDisk)) {
		t.Errorf("the gear's report says %q is %d bytes, and the file it landed at %q is %d",
			f.Name, f.Bytes, f.Path, len(onDisk))
	}
}

// checkCopiedBack is the plain case: the gear opened the file at in/<path> and
// wrote the same bytes into out/.
func checkCopiedBack(t *testing.T, d *door, l landed) {
	t.Helper()

	// The gear printed the path it opened. This is what separates "the gear was
	// told a name" from "the gear found a file there".
	if !reportsLine(l.res.Output, l.path) {
		t.Fatalf("the gear did not report opening %q; it printed:\n%s", l.path, l.res.Output)
	}
	if len(l.res.Files) != 1 {
		t.Fatalf("the gear handed back %d files for the one it was given: %+v (not taken: %v)",
			len(l.res.Files), l.res.Files, l.res.NotTaken)
	}
	back := readWorkspaceFile(t, d, l.res.Files[0].Path)
	if !bytes.Equal(back, l.body) {
		t.Fatalf("what the gear handed back is not what was sent to it: %s", difference(l.body, back))
	}
	checkReportedSize(t, l.res.Files[0], back)
}

// checkUnpacked is the archive case: every member came back out as its own file
// in the workspace, holding what was put into the archive.
func checkUnpacked(entries []zipEntry) func(*testing.T, *door, landed) {
	return func(t *testing.T, d *door, l landed) {
		t.Helper()

		// WHERE the archive was found, first — asserted here for the same reason
		// the other kinds assert it, and it was missing from this one. An
		// unpacking gear reports what was INSIDE the file, and that output is
		// identical whether the archive was staged at the path the caller named
		// or flattened into the root of in/. Only this line tells the two apart.
		if !reportsLine(l.res.Output, l.path) {
			t.Fatalf("the gear did not report opening the archive at %q; it printed:\n%s", l.path, l.res.Output)
		}
		for _, e := range entries {
			if !reportsLine(l.res.Output, e.name) {
				t.Errorf("the gear did not list %q among the archive's entries; it printed:\n%s", e.name, l.res.Output)
			}
		}
		if len(l.res.Files) != len(entries) {
			t.Fatalf("an archive of %d members came back as %d files: %+v (not taken: %v)",
				len(entries), len(l.res.Files), l.res.Files, l.res.NotTaken)
		}
		byName := map[string]producedFile{}
		for _, f := range l.res.Files {
			byName[f.Name] = f
		}
		for _, e := range entries {
			// The gear flattens a nested name so out/ stays a flat directory.
			flat := strings.ReplaceAll(e.name, "/", "__")
			produced, ok := byName[flat]
			if !ok {
				t.Errorf("%q was in the archive and did not come back out; what did: %v", e.name, byName)
				continue
			}
			back := readWorkspaceFile(t, d, produced.Path)
			if !bytes.Equal(back, e.body) {
				t.Errorf("%q came back out of the archive changed: %s", e.name, difference(e.body, back))
			}
			checkReportedSize(t, produced, back)
		}
	}
}

func readWorkspaceFile(t *testing.T, d *door, rel string) []byte {
	t.Helper()
	full := filepath.Join(d.wsRoot(), filepath.FromSlash(rel))
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("nothing at %q, which is where the workspace was told the file is: %v", rel, err)
	}
	return body
}

// difference says where two byte strings stop agreeing. Printing the whole of a
// binary file into a failure message buries the answer; printing only the
// lengths is the length check this file exists to avoid.
func difference(want, got []byte) string {
	n := min(len(want), len(got))
	for i := range n {
		if want[i] != got[i] {
			return fmt.Sprintf("%d bytes were sent and %d came back; they first differ at byte %d, "+
				"%#02x against %#02x (around it: %q against %q)",
				len(want), len(got), i, want[i], got[i], window(want, i), window(got, i))
		}
	}
	if len(want) == len(got) {
		return "they are in fact identical, so this message is a bug in the test"
	}
	return fmt.Sprintf("%d bytes were sent and %d came back; the shorter is a prefix of the longer, "+
		"so the file was cut off rather than mangled", len(want), len(got))
}

func window(b []byte, i int) []byte {
	return b[max(0, i-8):min(len(b), i+8)]
}

// theAnswer is what the scripted agent says once the gear has run. The delivery
// response has to carry this exact sentence, or the caller outside the install
// is not being told what the agent concluded.
const theAnswer = "the gear opened the delivered file and handed it back"

// handToGear scripts the agent: on the first call it takes the workspace path
// it was given and hands it to the gear, and on the next it answers.
//
// rel is empty for the inlet path, where the agent must find the path in its own
// opening turn — the same way a real one would, and a prompt that stopped
// naming it fails here exactly as an agent would fail on it.
func (d *door) handToGear(gearName, rel string) {
	d.provider.answers(func(n int, call modelCall) modelReply {
		if n > 1 {
			return says(theAnswer)
		}
		p := rel
		if p == "" {
			p = workspacePathFromTurn(call.userText())
		}
		if p == "" {
			d.provider.note("the opening turn never told the agent where the file is:\n%s", call.userText())
			return says(theAnswer)
		}
		return d.gearCall("c1", gearName, p)
	})
}

// gearCall is the tool call a model makes to put one workspace file in front of
// a gear. It runs on the provider's goroutine, so a failure to build it becomes
// a note the test fails on rather than a t.Fatal from the wrong goroutine.
func (d *door) gearCall(id, gearName, rel string) modelReply {
	args, err := json.Marshal(map[string]any{filesArg: []string{rel}})
	if err != nil {
		d.provider.note("build the call to gear %q: %v", gearName, err)
		return says(theAnswer)
	}
	return asksFor(modelToolCall{ID: id, Name: "gear_" + gearName, Args: string(args)})
}

// workspacePathFromTurn reads the delivered file's path out of the turn the
// delivery handler wrote for the agent.
func workspacePathFromTurn(turn string) string {
	const marker = "\n  path: "
	i := strings.Index(turn, marker)
	if i < 0 {
		return ""
	}
	line, _, _ := strings.Cut(turn[i+len(marker):], "\n")
	return strings.TrimSpace(line)
}

// gearResultOf digs the gear's answer out of the conversation as the provider
// received it — the second request carries the result of the first request's
// tool call — and fails with the raw text when it is not a gear result, because
// a gear that refused says so there and nowhere else.
func gearResultOf(t *testing.T, d *door, callN int) gearAnswer {
	t.Helper()
	return gearResultsOf(t, d, callN, 1)[0]
}

// gearResultsOf is gearResultOf for a turn with more than one gear call in it.
// The request numbered callN carries every tool result the turn has produced so
// far, in the order the agent asked for them, so a chain of gears is read out
// of one request rather than stitched together from several.
func gearResultsOf(t *testing.T, d *door, callN, want int) []gearAnswer {
	t.Helper()
	if n := d.provider.called(); n != callN {
		t.Fatalf("the model was called %d times, want %d: the agent did not call the gear and then answer", n, callN)
	}
	results := d.provider.call(t, callN).toolResults()
	if len(results) != want {
		t.Fatalf("the agent's gear calls came back as %d tool results, want %d: %v", len(results), want, results)
	}
	out := make([]gearAnswer, 0, len(results))
	for i, raw := range results {
		var res gearAnswer
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("gear call %d did not answer with the file report the model reads — what it said was:\n%s", i+1, raw)
		}
		if len(res.NotTaken) > 0 {
			t.Errorf("gear call %d produced files that were not collected: %v", i+1, res.NotTaken)
		}
		if strings.TrimSpace(res.Stderr) != "" {
			t.Errorf("gear call %d wrote to stderr: %s", i+1, res.Stderr)
		}
		out = append(out, res)
	}
	return out
}

// --- 1. The inlet: post a file, get the agent's answer back. ---------------

// TestFileRoundTripThroughAnInlet drives all six steps once per kind of file.
func TestFileRoundTripThroughAnInlet(t *testing.T) {
	t.Parallel()

	for _, c := range fileKinds(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.gear.runtime == "python" {
				needsPython(t)
			}
			d := newRunningDoor(t)
			d.forge(t, c.gear)
			d.addFileTask(t, "intake", c.accepts, workspace.OrchestratorName,
				"Hand the delivered file to the "+c.gear.name+" gear.")
			d.handToGear(c.gear.name, "")

			// STEP 1 — the receiver takes it. A caller with a filename sends the
			// multipart body a form or curl -F sends; a caller with none sends
			// the raw body, which is curl --data-binary.
			body, contentType := c.body, c.contentType
			if c.filename != "" {
				body, contentType = multipartFile(t, c.filename, c.contentType, c.body)
			}
			rec := d.deliver(t, "intake", d.key, contentType, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("delivering a %s: %d %s", c.name, rec.Code, rec.Body.String())
			}
			got := d.decode(t, rec)

			// STEP 2 — it is a file in the workspace, holding the posted bytes.
			row := d.run(t, got.Run)
			if row.PayloadPath == "" {
				t.Fatal("the ledger does not say where the delivered file went")
			}
			onDisk := readWorkspaceFile(t, d, row.PayloadPath)
			if !bytes.Equal(onDisk, c.body) {
				t.Fatalf("the file in the workspace is not the file that was posted: %s", difference(c.body, onDisk))
			}
			if !strings.HasPrefix(row.PayloadPath, workdir.InletDir+"/") {
				t.Errorf("a delivered file must land under %q, and this one is at %q", workdir.InletDir, row.PayloadPath)
			}
			// The name the caller chose, as this door is willing to keep it: the
			// run number, then a label with nothing in it that could name a
			// directory. Asserted exactly, because this is the one place a
			// caller's string is turned into a path on the operator's machine —
			// and this kind is only an awkward name honestly meant.
			// TestAHostileFilenameLandsAsALabelAndNowhereElse is the same
			// assertion for the names that are not.
			if want := fmt.Sprintf("%d-%s", got.Run, c.landsAs); filepath.Base(row.PayloadPath) != want {
				t.Errorf("the file landed as %q, and the door's cleaner should have named it %q",
					filepath.Base(row.PayloadPath), want)
			}

			// STEPS 3, 4 and 5 — the agent handed it to a real gear, the gear
			// opened the bytes, and what it produced is back in the workspace.
			res := gearResultOf(t, d, 2)
			c.check(t, d, landed{path: row.PayloadPath, body: c.body, res: res})

			// STEP 6 — the caller outside the install is told what the agent
			// concluded, not merely that something happened.
			if got.State != inlet.StateCompleted {
				t.Fatalf("the delivery ended as %q: %s", got.State, got.Error)
			}
			if got.Result != theAnswer {
				t.Fatalf("the delivery response carries %q, and the agent said %q", got.Result, theAnswer)
			}
			if notes := d.provider.complaints(); len(notes) > 0 {
				t.Fatalf("the provider complained: %s", strings.Join(notes, "; "))
			}
		})
	}
}

// --- 2. The chat path: the same file, attached to a message. ---------------

// TestFileRoundTripThroughTheChatPath is the same claim for the other door.
// An operator attaching a file and a caller delivering one are the same act —
// something from outside becoming a file inside the agent's reach — and the
// evidence that they really are the same is that one gear, unchanged, opens
// what either of them handed over.
func TestFileRoundTripThroughTheChatPath(t *testing.T) {
	t.Parallel()

	for _, c := range fileKinds(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.gear.runtime == "python" {
				needsPython(t)
			}
			d := newRunningDoor(t)
			d.forge(t, c.gear)
			// Declared able to take both, so the kinds a model CAN be shown go
			// down that route as well and this test is not quietly the
			// path-only test again.
			if _, err := d.cat.SetModelAccepts(context.Background(), d.modelID, []string{"image", "document"}); err != nil {
				t.Fatalf("declare what the model takes: %v", err)
			}

			// The composer always sends a filename; the raw-body case is an
			// inlet caller's shape, not an operator's, so it is named here —
			// and "receipt" needs no cleaning, so that is what it lands as.
			name, wantName := c.filename, c.landsAs
			if name == "" {
				name, wantName = "receipt", "receipt"
			}
			att := d.attachOK(t, name, c.body)

			// It landed in this workspace, holding the attached bytes.
			onDisk := readWorkspaceFile(t, d, att.Path)
			if !bytes.Equal(onDisk, c.body) {
				t.Fatalf("the attached file in the workspace is not the file that was uploaded: %s",
					difference(c.body, onDisk))
			}
			if !strings.HasPrefix(att.Path, workdir.AttachmentDir+"/") {
				t.Errorf("an attachment must land under %q, and this one is at %q", workdir.AttachmentDir, att.Path)
			}
			// The same name assertion the inlet makes, on the other door.
			// Both doors turn a caller's string into a path on the operator's
			// machine through one cleaner, and a prefix check would hold just
			// as well on a door that kept the operator's spaces, en dashes and
			// emoji — or their "../" — in a filename. Where the inlet puts the
			// run number, an attachment puts a directory, so what is compared
			// is the last segment.
			if got := path.Base(att.Path); got != wantName {
				t.Errorf("the attachment landed as %q, and the door's cleaner should have named it %q",
					got, wantName)
			}

			d.handToGear(c.gear.name, att.Path)
			if rec := d.chat(t, "hand this to the "+c.gear.name+" gear", att.Path); rec.Code != http.StatusOK {
				t.Fatalf("chatting with a %s attached: %d %s", c.name, rec.Code, rec.Body.String())
			}

			res := gearResultOf(t, d, 2)
			c.check(t, d, landed{path: att.Path, body: c.body, res: res})
			if notes := d.provider.complaints(); len(notes) > 0 {
				t.Fatalf("the provider complained: %s", strings.Join(notes, "; "))
			}
		})
	}
}

// --- 3. One gear into the next. -------------------------------------------

// TestFileRoundTripChainsOneGearIntoTheNext makes true the sentence a pipeline
// is built on. gear.Produced says of Path that it is "the same form the caller
// uses to hand it to the next gear" (internal/gear/files.go) — and until this
// test nothing ever handed one back in. Every other case here stops at the
// moment a gear produced a file, which is one step short of what an operator
// actually does: the archive is unpacked SO THAT something can be done with
// what was inside it.
//
// The path gear B is called with is read out of gear A's file report, by the
// scripted agent, on the provider's own goroutine, exactly as a model would
// read it. It is never built by the test out of what the test believes the
// naming rule to be — a test that composed the path itself would keep passing
// on a build whose report named files that are not where it says they are,
// which is the whole failure this is here to catch.
func TestFileRoundTripChainsOneGearIntoTheNext(t *testing.T) {
	t.Parallel()
	needsPython(t)

	d := newRunningDoor(t)
	d.forge(t, unpackGear)
	d.forge(t, carryGear)

	entries := zipContents(t)
	// The nested member: it is flattened on the way out of the archive, so the
	// name gear A reports is one no other step of this test could have produced
	// by accident, and the bytes are not the bytes of the archive itself.
	const member, flattened = "data/rows.csv", "data__rows.csv"
	var inside []byte
	for _, e := range entries {
		if e.name == member {
			inside = e.body
		}
	}
	if inside == nil {
		t.Fatalf("the test archive no longer has a %q in it; this test needs a nested member", member)
	}

	d.addFileTask(t, "intake", "application/zip", workspace.OrchestratorName,
		"Unpack the delivered archive with the unpack gear, then hand one of the files it produced to the carry gear.")

	d.provider.answers(func(n int, call modelCall) modelReply {
		switch n {
		case 1:
			rel := workspacePathFromTurn(call.userText())
			if rel == "" {
				d.provider.note("the opening turn never told the agent where the archive is:\n%s", call.userText())
				return says(theAnswer)
			}
			return d.gearCall("c1", unpackGear.name, rel)
		case 2:
			// The first gear's answer, as the provider received it. The second
			// call's argument comes out of this report and out of nothing else.
			results := call.toolResults()
			if len(results) != 1 {
				d.provider.note("the unpack gear's answer arrived as %d tool results: %v", len(results), results)
				return says(theAnswer)
			}
			var res gearAnswer
			if err := json.Unmarshal([]byte(results[0]), &res); err != nil {
				d.provider.note("the unpack gear did not answer with a file report:\n%s", results[0])
				return says(theAnswer)
			}
			for _, f := range res.Files {
				if f.Name == flattened {
					return d.gearCall("c2", carryGear.name, f.Path)
				}
			}
			d.provider.note("the unpack gear's report names no %q, so there is no path to hand on: %+v", flattened, res.Files)
			return says(theAnswer)
		}
		return says(theAnswer)
	})

	body, contentType := multipartFile(t, "books.zip", "application/zip", zipBytes(t, entries))
	rec := d.deliver(t, "intake", d.key, contentType, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("delivering the archive: %d %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)

	// Three model calls: unpack, carry, answer — and the third request carries
	// both gears' results.
	results := gearResultsOf(t, d, 3, 2)
	unpacked, carried := results[0], results[1]

	var produced producedFile
	for _, f := range unpacked.Files {
		if f.Name == flattened {
			produced = f
		}
	}
	if produced.Path == "" {
		t.Fatalf("the unpack gear did not produce %q, so there was nothing to chain: %+v", flattened, unpacked.Files)
	}

	// The link itself: the second gear reports opening the exact path the first
	// gear reported writing. That line is the gear's own account of where it
	// found the file, so a Path that names somewhere the file is not fails here
	// rather than being taken on trust.
	if !reportsLine(carried.Output, produced.Path) {
		t.Fatalf("the second gear did not open %q, which is the path the first gear said it produced; it printed:\n%s",
			produced.Path, carried.Output)
	}
	if len(carried.Files) != 1 {
		t.Fatalf("the second gear was given one file and handed back %d: %+v (not taken: %v)",
			len(carried.Files), carried.Files, carried.NotTaken)
	}
	// And the bytes, which are what "it read the right file" means. Compared
	// against what went INTO the archive: everything between here and there —
	// the unpacking, the collection, the staging, the second collection — is
	// only correct if these are the same bytes.
	back := readWorkspaceFile(t, d, carried.Files[0].Path)
	if !bytes.Equal(back, inside) {
		t.Fatalf("what the second gear carried out is not what was inside the archive: %s", difference(inside, back))
	}
	checkReportedSize(t, carried.Files[0], back)
	if carried.Files[0].Path == produced.Path {
		t.Errorf("the second gear's output landed on top of the file it was given (%q); a run directory per call is what stops that", produced.Path)
	}

	if got.State != inlet.StateCompleted {
		t.Fatalf("the delivery ended as %q: %s", got.State, got.Error)
	}
	if notes := d.provider.complaints(); len(notes) > 0 {
		t.Fatalf("the provider complained: %s", strings.Join(notes, "; "))
	}
}

// --- 4. The name a caller chooses. ----------------------------------------

// TestAHostileFilenameLandsAsALabelAndNowhereElse drives the cleaner through
// the two doors that call it, with the names somebody hostile sends rather than
// the one awkward name an honest caller sends.
//
// safeFileName is described in this file as "the one place a caller's string is
// turned into a path on the operator's machine", and the only name tested here
// was a unicode one — which proves a label is KEPT and says nothing about a
// direction being dropped. These are the directions. What is asserted is the
// exact path each one lands at, because "it is safe" is not a thing a test can
// check, and then that the deliveries put bytes nowhere but inside the
// workspace they belong to.
func TestAHostileFilenameLandsAsALabelAndNowhereElse(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addFileTask(t, "intake", "text/plain", workspace.OrchestratorName, "File this.")
	d.provider.answers(func(n int, call modelCall) modelReply { return says("filed") })

	body := []byte("the name on this file was chosen by somebody who is not the operator\n")
	before := filesUnder(t, d.dir)

	for _, c := range []struct{ name, filename, landsAs string }{
		{"a relative traversal", "../../etc/passwd", "passwd"},
		{"an absolute path", "/etc/cron.d/evil.sh", "evil.sh"},
		// Backslashes, because a POSIX path.Base sees one Windows path as a
		// single segment and would keep the lot.
		{"a windows path", `C:\Users\operator\..\..\Windows\System32\evil.exe`, "evil.exe"},
		// Past the 80-character cap: what is kept is a prefix, and the
		// extension is not preserved — a name this long is a limit somebody's
		// filesystem would otherwise enforce, badly.
		{"a name past the length cap", strings.Repeat("a", 200) + ".txt", strings.Repeat("a", 80)},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The inlet door: the run number, then the label.
			payload, contentType := multipartFile(t, c.filename, "text/plain", body)
			rec := d.deliver(t, "intake", d.key, contentType, payload)
			if rec.Code != http.StatusOK {
				t.Fatalf("delivering a file named %q: %d %s — the name is neutered, not a reason to refuse",
					c.filename, rec.Code, rec.Body.String())
			}
			got := d.decode(t, rec)
			row := d.run(t, got.Run)
			want := path.Join(workdir.InletDir, d.address, fmt.Sprintf("%d-%s", got.Run, c.landsAs))
			if row.PayloadPath != want {
				t.Errorf("a file named %q landed at %q, and the door's cleaner should have put it at %q",
					c.filename, row.PayloadPath, want)
			}
			if landed := readWorkspaceFile(t, d, row.PayloadPath); !bytes.Equal(landed, body) {
				t.Errorf("the file at %q is not what was delivered: %s", row.PayloadPath, difference(body, landed))
			}

			// The other door, with the same name: an attachment's uniqueness is
			// in the directory, so the last segment is the whole of the label.
			att := d.attachOK(t, c.filename, body)
			if base := path.Base(att.Path); base != c.landsAs {
				t.Errorf("attached as %q, the file landed as %q, and the door's cleaner should have named it %q",
					c.filename, base, c.landsAs)
			}
			if !strings.HasPrefix(att.Path, workdir.AttachmentDir+"/") {
				t.Errorf("an attachment named %q landed at %q, outside %q", c.filename, att.Path, workdir.AttachmentDir)
			}
		})
	}

	// A NUL in the name is refused at the door and never reaches the cleaner.
	//
	// safeFileName strips NUL bytes, and through these doors it never has to:
	// a filename is carried in a MIME header, and Go's multipart reader will
	// not parse a header line with a NUL in it, so the whole body is refused
	// before a byte is written. That is a stricter answer than landing under a
	// cleaned name, and it is asserted here as what actually happens rather
	// than as what the cleaner would have done — the cleaner's own NUL case is
	// covered where the cleaner is called directly.
	t.Run("a name with a NUL in it", func(t *testing.T) {
		payload, contentType := multipartFile(t, "ev\x00il.txt", "text/plain", body)
		rec := d.deliver(t, "intake", d.key, contentType, payload)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("a filename with a NUL in it: %d, want 400\nbody: %s", rec.Code, rec.Body.String())
		}
		got := d.decode(t, rec)
		if row := d.run(t, got.Run); row.PayloadPath != "" {
			t.Errorf("a refused delivery still wrote a file, at %q", row.PayloadPath)
		}
		if rec := d.attach(t, "ev\x00il.txt", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("attaching a file whose name holds a NUL: %d, want 400\nbody: %s", rec.Code, rec.Body.String())
		}
	})

	// Nothing left the workspace. This looks for the BYTES rather than for a
	// name, because a file that escaped is not at any path this test would
	// think to check and will not be called what the caller called it — but it
	// holds what was delivered.
	root := d.wsRoot()
	for p := range filesUnder(t, d.dir) {
		if before[p] || strings.HasPrefix(p, root+string(filepath.Separator)) {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, body) {
			t.Errorf("a delivered file's bytes were written outside its workspace, at %s", p)
		}
	}
}

// filesUnder is every ordinary file under a directory. Comparing the set before
// and after is what catches a write nobody would think to look for: an
// assertion about a path can only ever check the path it already expects.
func filesUnder(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			out[p] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the install's data directory: %v", err)
	}
	return out
}

// --- 5. The other direction: what the provider is actually shown. ----------

// TestFileRoundTripToTheModel asserts on the request body the stub received.
// Everything above is about what a gear can open; this is about what crosses
// the wire, and the only witness to that is the bytes the provider was sent.
func TestFileRoundTripToTheModel(t *testing.T) {
	t.Parallel()

	t.Run("a png arrives as an image part", func(t *testing.T) {
		d := newDoor(t)
		d.takesImages(t)
		pixels := pngBytes(t)

		att := d.attachOK(t, "pixels.png", pixels)
		if att.Kind != "image" {
			t.Fatalf("a PNG should go to the model as an image, and it went as %q (skipped: %s)", att.Kind, att.Skipped)
		}
		if rec := d.chat(t, "what is in this?", att.Path); rec.Code != http.StatusOK {
			t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
		}

		// The bytes, in the encoding the protocol carries them in, in the body
		// that actually left this process. Not our Part type, and not a decoded
		// view of it that would agree with a build sending nothing at all.
		raw := d.provider.call(t, 1).Raw
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixels)
		if !strings.Contains(raw, want) {
			t.Fatalf("the PNG's own bytes are not in the request the provider received:\n%s", raw)
		}
		if n := len(imageParts(d.provider.call(t, 1))); n != 1 {
			t.Fatalf("the request carries %d image parts, want 1", n)
		}
	})

	t.Run("a pdf arrives as a document part", func(t *testing.T) {
		d := newDoor(t)
		if _, err := d.cat.SetModelAccepts(context.Background(), d.modelID, []string{"document"}); err != nil {
			t.Fatalf("declare the model able to take documents: %v", err)
		}
		doc := pdfBytes(t)

		att := d.attachOK(t, "report.pdf", doc)
		if att.Kind != "document" {
			t.Fatalf("a PDF should go to the model as a document, and it went as %q (skipped: %s)", att.Kind, att.Skipped)
		}
		if rec := d.chat(t, "summarise this", att.Path); rec.Code != http.StatusOK {
			t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
		}

		raw := d.provider.call(t, 1).Raw
		want := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(doc)
		if !strings.Contains(raw, want) {
			t.Fatalf("the PDF's own bytes are not in the request the provider received:\n%s", raw)
		}
		if got := filePartFilenames(d.provider.call(t, 1)); len(got) != 1 || got[0] != "report.pdf" {
			t.Fatalf("the request carries file parts %v, want just report.pdf", got)
		}
	})

	t.Run("a zip is refused before the request, and the refusal names the gear route", func(t *testing.T) {
		d := newDoor(t)
		archive := zipBytes(t, zipContents(t))

		// The refusal is produced while the file is being landed — before any
		// turn is built — and at that moment the provider has been contacted
		// exactly not at all.
		att := d.attachOK(t, "books.zip", archive)
		if att.Kind != "" {
			t.Fatalf("no model can look inside a zip, and this one was sent as %q", att.Kind)
		}
		if n := d.provider.called(); n != 0 {
			t.Fatalf("the archive reached a provider %d time(s) before anything decided it could be shown", n)
		}
		// The operator and the agent both need the next step, not a diagnosis.
		//
		// Asserted as the whole instruction rather than as the word "gear",
		// which is four letters that turn up in almost any sentence this system
		// writes about a file — "no gear ran", "the gear failed", "gears are
		// disabled" would all have satisfied the old check while telling a
		// reader nothing they could act on. What has to survive is the route:
		// which file, what it is, that it is still in the workspace, and that
		// its PATH is what goes to a gear.
		for _, want := range []string{
			`"books.zip"`,
			`"application/zip"`,
			"The file is in the workspace: give its path to a gear that reads this format",
		} {
			if !strings.Contains(att.Skipped, want) {
				t.Errorf("the refusal does not say %q, so nobody reading it knows what to do next: %q", want, att.Skipped)
			}
		}

		// And the turn that follows carries the path and none of the archive.
		// Searched in the raw body rather than in the parts we know the names
		// of: the risk is a shape nobody here thought of.
		if rec := d.chat(t, "what is in this archive?", att.Path); rec.Code != http.StatusOK {
			t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
		}
		raw := d.provider.call(t, 1).Raw
		if strings.Contains(raw, base64.StdEncoding.EncodeToString(archive)) {
			t.Fatalf("the archive's bytes were sent to the provider anyway:\n%s", raw)
		}
		for _, smuggle := range []string{";base64,", "image_url", "file_data", `"source"`} {
			if strings.Contains(raw, smuggle) {
				t.Errorf("the request carries %q, so something file-shaped is being sent for a zip:\n%s", smuggle, raw)
			}
		}
		if !strings.Contains(raw, att.Path) {
			t.Errorf("the turn does not name the archive's path, so the agent cannot reach it either:\n%s", raw)
		}
	})

	t.Run("a model not declared as taking images refuses the png before the request", func(t *testing.T) {
		d := newDoor(t)
		att := d.attachOK(t, "pixels.png", pngBytes(t))

		if rec := d.chat(t, "look at this", att.Path); rec.Code != http.StatusOK {
			t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
		}
		if n := d.provider.called(); n != 0 {
			t.Fatalf("the model was called %d time(s) with a file nobody said it could take", n)
		}

		// The refusal has to reach the operator and name both halves of the
		// problem: which file, and which model.
		msgs, err := d.spaces.ListMessages(context.Background(), d.wsID, nil, 100)
		if err != nil {
			t.Fatalf("read the timeline: %v", err)
		}
		var refusal string
		for _, m := range msgs {
			if m.Kind == "error" {
				refusal = m.Content
			}
		}
		if refusal == "" {
			t.Fatalf("nothing on the timeline says why the image was not sent: %+v", msgs)
		}
		for _, want := range []string{"pixels.png", "house / test-model"} {
			if !strings.Contains(refusal, want) {
				t.Errorf("the refusal does not name %q: %s", want, refusal)
			}
		}
	})
}
