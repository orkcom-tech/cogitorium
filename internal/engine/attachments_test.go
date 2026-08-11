package engine

// What becomes of an attached file, against real files in a real directory.
// The classification reads bytes off the disk and asks llm what a model can be
// shown, so there is nothing here to stand in for: the files are the input.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// workspaceWith writes files into a workspace directory and returns an engine
// pointed at it.
func workspaceWith(t *testing.T, files map[string][]byte) (*Engine, int64) {
	t.Helper()
	e := &Engine{dataDir: t.TempDir()}
	const wsID = int64(1)
	root := workdir.Dir(e.dataDir, wsID)
	if root == "" {
		t.Fatal("the workspace directory could not be created")
	}
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("prepare %q: %v", name, err)
		}
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	return e, wsID
}

func TestAttachedTextIsSentLabelledWithItsPath(t *testing.T) {
	e, wsID := workspaceWith(t, map[string][]byte{
		"attachments/notes.md": []byte("# what we agreed\n"),
	})

	atts, parts, err := e.collectAttachments(wsID, []string{"attachments/notes.md"})
	if err != nil {
		t.Fatalf("attach a text file: %v", err)
	}
	if len(parts) != 1 || parts[0].Kind != llm.PartText {
		t.Fatalf("a markdown file should be shown to the model as text, got %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "attachments/notes.md") {
		t.Errorf("the text was sent unlabelled, so the model cannot tell which file it is reading:\n%s", parts[0].Text)
	}
	if !strings.Contains(parts[0].Text, "# what we agreed") {
		t.Errorf("the file's own content did not survive the labelling:\n%s", parts[0].Text)
	}
	if atts[0].Kind != llm.PartText || atts[0].Skipped != "" {
		t.Errorf("the record disagrees with what was sent: %+v", atts[0])
	}
}

// A file past the size a model may be shown is not an error and does not stop
// the message: it goes the way every file a model cannot read goes.
func TestOversizedAttachmentBecomesAPathAndSaysWhy(t *testing.T) {
	e, wsID := workspaceWith(t, map[string][]byte{
		"attachments/dump.txt": make([]byte, llm.MaxPartBytes+1),
	})

	atts, parts, err := e.collectAttachments(wsID, []string{"attachments/dump.txt"})
	if err != nil {
		t.Fatalf("attaching a large file must not fail the message: %v", err)
	}
	if len(parts) != 0 {
		t.Fatalf("a file over the cap was sent to the model anyway")
	}
	if atts[0].ToModel() {
		t.Fatalf("the record claims the model was shown it: %+v", atts[0])
	}
	if !strings.Contains(atts[0].Skipped, "gear") {
		t.Errorf("the operator is not told what to do instead: %q", atts[0].Skipped)
	}

	// And the note the agent reads still carries the path, which is the whole
	// point of not refusing.
	note := attachmentNote(atts)
	if !strings.Contains(note, "attachments/dump.txt") {
		t.Fatalf("the agent is not told where the file is:\n%s", note)
	}
	if !strings.Contains(note, gearFilesArg) {
		t.Fatalf("the note does not name the route that works:\n%s", note)
	}
}

func TestEmptyAttachmentIsNotSentToTheModel(t *testing.T) {
	e, wsID := workspaceWith(t, map[string][]byte{"attachments/nothing.txt": {}})

	atts, parts, err := e.collectAttachments(wsID, []string{"attachments/nothing.txt"})
	if err != nil {
		t.Fatalf("attaching an empty file must not fail the message: %v", err)
	}
	if len(parts) != 0 {
		t.Fatalf("an empty file was sent to the model")
	}
	if atts[0].Skipped == "" {
		t.Fatalf("the record does not say why it was not sent: %+v", atts[0])
	}
}

// The byte budget for one message is spent in the order the files were
// attached, and what does not fit takes the path route rather than failing the
// message or being sent anyway.
func TestOneMessageStopsAtTheRequestByteCap(t *testing.T) {
	big := make([]byte, llm.MaxPartBytes)
	for i := range big {
		big[i] = 'a'
	}
	files := map[string][]byte{}
	var paths []string
	for i := 0; i < (llm.MaxRequestPartBytes/llm.MaxPartBytes)+1; i++ {
		name := filepath.ToSlash(filepath.Join("attachments", string(rune('a'+i))+".txt"))
		files[name] = big
		paths = append(paths, name)
	}
	e, wsID := workspaceWith(t, files)

	atts, parts, err := e.collectAttachments(wsID, paths)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	var sent int64
	for _, p := range parts {
		sent += partBytes(p)
	}
	if sent > llm.MaxRequestPartBytes {
		t.Fatalf("one request would carry %d bytes, over the %d cap", sent, llm.MaxRequestPartBytes)
	}
	last := atts[len(atts)-1]
	if last.ToModel() {
		t.Fatalf("the file past the cap was sent anyway: %+v", last)
	}
	if !strings.Contains(last.Skipped, "gear") {
		t.Errorf("the file past the cap is not routed anywhere: %q", last.Skipped)
	}
}

// A message with no files on it is untouched — the same text, and no meta.
func TestNoAttachmentsChangesNothing(t *testing.T) {
	if got := withAttachments("just a question", nil); got != "just a question" {
		t.Fatalf("a plain message was rewritten: %q", got)
	}
	meta, err := attachmentMeta(nil)
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if meta != "" {
		t.Fatalf("a plain message would carry meta %q", meta)
	}
}

// A message with a file and no words is an ordinary thing to send.
func TestAttachmentWithNoWordsStillSaysSomething(t *testing.T) {
	atts := []Attachment{{Path: "attachments/shot.png", MediaType: "image/png", Bytes: 68, Kind: llm.PartImage}}
	got := withAttachments("", atts)
	if !strings.Contains(got, "attachments/shot.png") {
		t.Fatalf("an empty message with a file on it says nothing at all:\n%s", got)
	}
	if strings.HasPrefix(got, "\n") {
		t.Fatalf("the turn opens with blank lines where the operator's text would be: %q", got)
	}
}
