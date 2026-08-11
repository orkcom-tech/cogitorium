package server

// Attaching files to a message to the orchestrator, end to end: a real
// database, a real workspace directory on disk, and a provider answering over
// a real socket. What is asserted here is what the provider actually received,
// because the whole feature is about what reaches a model and what does not.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// onePixelPNG is a real PNG — 68 bytes, one pixel. A made-up byte string would
// prove nothing about a path whose whole job is carrying a file unchanged.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// attach uploads one file the way the composer does: a multipart part with a
// filename, through the whole authenticated handler chain.
func (d *door) attach(t *testing.T, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("build the multipart body: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write the multipart body: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close the multipart body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+id(d.wsID)+"/attachments", &buf)
	req.RemoteAddr = offBox
	req.Header.Set("Authorization", "Bearer "+d.adminTok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

type attached struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	Kind      string `json:"kind"`
	Skipped   string `json:"skipped"`
}

func (d *door) attachOK(t *testing.T, name string, body []byte) attached {
	t.Helper()
	rec := d.attach(t, name, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("attaching %q: %d %s", name, rec.Code, rec.Body.String())
	}
	var out attached
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the attachment answer is not an attachment: %s", rec.Body.String())
	}
	return out
}

// chat sends one operator message, attachments and all, and runs the turn.
func (d *door) chat(t *testing.T, text string, attachments ...string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"text": text, "attachments": attachments})
	if err != nil {
		t.Fatalf("build the chat body: %v", err)
	}
	return d.request(t, http.MethodPost, "/api/v1/workspaces/"+id(d.wsID)+"/chat", d.adminTok, string(body))
}

// userContents returns every user turn of one model call exactly as the
// provider received it — a plain string when the turn is text, and the raw
// parts array when it carries files.
func userContents(call modelCall) []any {
	var out []any
	for _, m := range call.Messages {
		if m["role"] == "user" {
			out = append(out, m["content"])
		}
	}
	return out
}

// imageParts digs the base64 image parts out of a call, which is the one thing
// that proves the bytes were really sent rather than described.
func imageParts(call modelCall) []string {
	var out []string
	for _, content := range userContents(call) {
		parts, ok := content.([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok || part["type"] != "image_url" {
				continue
			}
			img, _ := part["image_url"].(map[string]any)
			url, _ := img["url"].(string)
			out = append(out, url)
		}
	}
	return out
}

// userProse is every word of user text in a call, whatever shape the turn is,
// so an assertion about what the model was TOLD does not have to know whether
// the turn also carried bytes.
func userProse(call modelCall) string {
	var b strings.Builder
	for _, content := range userContents(call) {
		switch v := content.(type) {
		case string:
			b.WriteString(v)
		case []any:
			for _, p := range v {
				part, ok := p.(map[string]any)
				if !ok || part["type"] != "text" {
					continue
				}
				text, _ := part["text"].(string)
				b.WriteString(text)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (d *door) takesImages(t *testing.T) {
	t.Helper()
	if _, err := d.cat.SetModelAccepts(context.Background(), d.modelID, []string{"image"}); err != nil {
		t.Fatalf("declare the model able to take images: %v", err)
	}
}

// An image goes to the model on the turn it is attached to, and never again.
// The second half is the point: re-encoding a picture into every later request
// is a bill that grows with the length of the conversation, and the path in the
// note is what the agent uses instead.
func TestAttachedImageIsSentOnceAndReplayedAsAPath(t *testing.T) {
	d := newDoor(t)
	d.takesImages(t)

	att := d.attachOK(t, "diagram.png", onePixelPNG)
	if att.Kind != "image" {
		t.Fatalf("a PNG should go to the model as an image, got kind %q (skipped: %s)", att.Kind, att.Skipped)
	}
	if att.Bytes != int64(len(onePixelPNG)) {
		t.Errorf("the attachment is %d bytes, the file was %d", att.Bytes, len(onePixelPNG))
	}
	if !strings.HasPrefix(att.Path, workdir.AttachmentDir+"/") {
		t.Errorf("an attachment should land under %q, got %q", workdir.AttachmentDir, att.Path)
	}

	// It is a file in the workspace, byte for byte, before anything else is
	// claimed about it: that is what makes the gear route possible for the
	// formats a model cannot read.
	onDisk, err := os.ReadFile(filepath.Join(workdir.Dir(d.dir, d.wsID), filepath.FromSlash(att.Path)))
	if err != nil {
		t.Fatalf("the attachment is not in the workspace: %v", err)
	}
	if !bytes.Equal(onDisk, onePixelPNG) {
		t.Fatalf("the file in the workspace is not the file that was attached")
	}

	if rec := d.chat(t, "what is in this?", att.Path); rec.Code != http.StatusOK {
		t.Fatalf("chat with an attachment: %d %s", rec.Code, rec.Body.String())
	}

	first := d.provider.call(t, 1)
	images := imageParts(first)
	if len(images) != 1 {
		t.Fatalf("the model should have been sent exactly one image, got %d", len(images))
	}
	if !strings.HasPrefix(images[0], "data:image/png;base64,") {
		t.Fatalf("the image did not arrive as a png data URL: %.40s", images[0])
	}
	if prose := userProse(first); !strings.Contains(prose, att.Path) {
		t.Fatalf("the turn carrying the image never named its path, so the agent could not hand it to a gear:\n%s", prose)
	}

	// The next message: the picture must not come with it.
	if rec := d.chat(t, "and what about now?"); rec.Code != http.StatusOK {
		t.Fatalf("second chat: %d %s", rec.Code, rec.Body.String())
	}
	second := d.provider.call(t, 2)
	if n := len(imageParts(second)); n != 0 {
		t.Fatalf("the image was re-sent on a later turn (%d image parts); every turn of a long conversation would pay for it", n)
	}
	prose := userProse(second)
	if !strings.Contains(prose, att.Path) {
		t.Fatalf("the replayed conversation lost the attachment entirely — the agent can no longer reach the file:\n%s", prose)
	}
	if !strings.Contains(prose, "and what about now?") {
		t.Fatalf("the second message is missing from the replay:\n%s", prose)
	}
	if c := d.provider.complaints(); len(c) > 0 {
		t.Fatalf("the provider complained: %v", c)
	}
}

// A file no model can look inside is not a failure. It lands, it is named, and
// the agent is told where it is — which is the only half of this that the gear
// route needs.
func TestAttachedBinaryReachesTheAgentAsAPath(t *testing.T) {
	d := newDoor(t)

	archive := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0x00, 0xff}, 64)...)
	att := d.attachOK(t, "books.zip", archive)
	if att.Kind != "" {
		t.Fatalf("no model can look inside a zip, but it was sent as %q", att.Kind)
	}
	if !strings.Contains(att.Skipped, "gear") {
		t.Fatalf("the refusal must tell the operator what to do instead; got %q", att.Skipped)
	}

	if rec := d.chat(t, "what is in this archive?", att.Path); rec.Code != http.StatusOK {
		t.Fatalf("chat with a binary attachment: %d %s", rec.Code, rec.Body.String())
	}
	call := d.provider.call(t, 1)
	if n := len(imageParts(call)); n != 0 {
		t.Fatalf("a zip was sent to the model as %d image parts", n)
	}
	prose := userProse(call)
	if !strings.Contains(prose, att.Path) {
		t.Fatalf("the agent was never told where the file is:\n%s", prose)
	}
	if !strings.Contains(prose, "not shown to you") {
		t.Fatalf("the agent was not told that it is not being shown the file, so it will answer as if it had seen it:\n%s", prose)
	}

	onDisk, err := os.ReadFile(filepath.Join(workdir.Dir(d.dir, d.wsID), filepath.FromSlash(att.Path)))
	if err != nil {
		t.Fatalf("the file a gear is supposed to open is not there: %v", err)
	}
	if !bytes.Equal(onDisk, archive) {
		t.Fatalf("the archive in the workspace is not the archive that was attached")
	}
}

// A model nobody declared able to take an image is not sent one. The refusal
// names the model and reaches the operator's timeline; the provider is never
// dialled, so the bill and the unactionable 400 both stay unspent.
func TestImageIsRefusedForAModelDeclaredTextOnly(t *testing.T) {
	d := newDoor(t)

	att := d.attachOK(t, "shot.png", onePixelPNG)
	if rec := d.chat(t, "look at this", att.Path); rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	if n := d.provider.called(); n != 0 {
		t.Fatalf("the model was called %d times with a file it was never said to accept", n)
	}

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
		t.Fatalf("nothing on the timeline says why the file was not sent: %+v", msgs)
	}
	for _, want := range []string{"shot.png", "house / test-model"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal does not name %q, so the operator cannot act on it: %s", want, refusal)
		}
	}
}

// A message pointing at a file that is not there is refused before anything is
// written: a half-message on the timeline, answered without the file the
// operator meant, is worse than being told to attach it again.
func TestChatRefusesAnAttachmentThatIsNotThere(t *testing.T) {
	d := newDoor(t)

	before := d.timelineLength(t)
	rec := d.chat(t, "read this", workdir.AttachmentDir+"/nothing-was-ever-here.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("the stream itself should open: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nothing-was-ever-here.png") {
		t.Fatalf("the operator was not told which file is missing: %s", rec.Body.String())
	}
	if n := d.timelineLength(t); n != before {
		t.Fatalf("the timeline grew by %d rows for a message that was never sent", n-before)
	}
	if n := d.provider.called(); n != 0 {
		t.Fatalf("the model was called %d times for a message that could not be assembled", n)
	}
}

// An attachment cannot name a path outside the workspace, whatever it spells.
func TestChatRefusesAnAttachmentOutsideTheWorkspace(t *testing.T) {
	d := newDoor(t)

	// The install's data directory, one level above this workspace's own — the
	// nearest thing worth stealing that is not inside it.
	secret := "the api keys nobody outside this machine should see"
	if err := os.WriteFile(filepath.Join(d.dir, "secrets.txt"), []byte(secret), 0o600); err != nil {
		t.Fatalf("write the file to reach for: %v", err)
	}

	before := d.timelineLength(t)
	rec := d.chat(t, "read this", "../../secrets.txt")
	if body := rec.Body.String(); strings.Contains(body, secret) {
		t.Fatalf("a file outside the workspace was read: %s", body)
	}
	// workdir.Clean collapses the "..", so this resolves to a name inside the
	// workspace that does not exist — refused as any missing attachment is.
	// The assertion is on the outcome rather than on which sentence came back:
	// there is no turn and there is no file.
	if n := d.timelineLength(t); n != before {
		t.Fatalf("a message naming a path outside the workspace still wrote %d rows", n-before)
	}
	if n := d.provider.called(); n != 0 {
		t.Fatalf("the model was called %d times for a path outside the workspace", n)
	}
}

// The timeline records what was attached, because a conversation where the
// operator's file is invisible is one nobody can read back.
func TestTimelineRecordsWhatWasAttached(t *testing.T) {
	d := newDoor(t)
	d.takesImages(t)

	att := d.attachOK(t, "diagram.png", onePixelPNG)
	if rec := d.chat(t, "here", att.Path); rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}

	msgs, err := d.spaces.ListMessages(context.Background(), d.wsID, nil, 100)
	if err != nil {
		t.Fatalf("read the timeline: %v", err)
	}
	var meta struct {
		Attachments []attached `json:"attachments"`
	}
	for _, m := range msgs {
		if m.Kind != "user" {
			continue
		}
		if err := json.Unmarshal([]byte(m.Meta), &meta); err != nil {
			t.Fatalf("the user row's meta is not JSON: %s", m.Meta)
		}
		if m.Content != "here" {
			t.Errorf("the operator's own words were rewritten on the way to the timeline: %q", m.Content)
		}
	}
	if len(meta.Attachments) != 1 || meta.Attachments[0].Path != att.Path {
		t.Fatalf("the timeline does not say what was attached: %+v", meta.Attachments)
	}
	if meta.Attachments[0].Kind != "image" {
		t.Fatalf("the timeline does not record that the model was shown it: %+v", meta.Attachments[0])
	}
}

// A message with no files on it is the message it always was: empty meta, and
// a plain string on the wire rather than a one-element parts array.
func TestPlainMessageIsUnchangedByAttachmentSupport(t *testing.T) {
	d := newDoor(t)

	if rec := d.chat(t, "hello"); rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	for _, content := range userContents(d.provider.call(t, 1)) {
		if _, ok := content.(string); !ok {
			t.Fatalf("a text-only turn crossed the wire as %T instead of a plain string", content)
		}
	}
	msgs, err := d.spaces.ListMessages(context.Background(), d.wsID, nil, 100)
	if err != nil {
		t.Fatalf("read the timeline: %v", err)
	}
	for _, m := range msgs {
		if m.Kind == "user" && m.Meta != "{}" {
			t.Fatalf("a message with no files on it carries meta %q", m.Meta)
		}
	}
}

// Nothing on the attachment route is reachable without the workspace's own
// access rule — it writes into the workspace directory, which is the terminal's
// blast radius and the Files page's.
func TestAttachmentNeedsAccessToTheWorkspace(t *testing.T) {
	tm := seedTeam(t, "0.0.0.0:8688")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "note.txt")
	fmt.Fprint(part, "not yours")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+id(tm.alicePrivate.ID)+"/attachments", &buf)
	req.RemoteAddr = offBox
	req.Header.Set("Authorization", "Bearer "+tm.bobTok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	tm.srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Fatalf("bob attached a file to alice's workspace: %d %s", rec.Code, rec.Body.String())
	}
}
