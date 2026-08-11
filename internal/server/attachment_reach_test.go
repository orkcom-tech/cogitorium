package server

// Two claims that only hold end to end, and are asserted on the request body a
// provider received over a real socket.
//
// The first is what a later turn does NOT carry. That one cannot be made from a
// decoded shape: proving an image is absent by looking for image parts assumes
// the only way to smuggle a file is under a name this test already knows, and
// the whole risk is a shape nobody thought of. So it searches the bytes.
//
// The second is that every attachment, whatever its type, is a file the agent
// can reach — through the tools, in a real turn, against the real dispatch.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// filePartFilenames digs out the OpenAI "file" parts — how a PDF crosses this
// protocol — so a test can tell a document that was sent from one that was only
// described.
func filePartFilenames(call modelCall) []string {
	var out []string
	for _, content := range userContents(call) {
		parts, ok := content.([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok || part["type"] != "file" {
				continue
			}
			file, _ := part["file"].(map[string]any)
			name, _ := file["filename"].(string)
			out = append(out, name)
		}
	}
	return out
}

// A file goes across the wire on the turn it was attached to and on no later
// one. The first request is checked for the bytes as well, because a test that
// only proves the second request lacks them would pass just as well on a build
// that never sent the file at all.
func TestASecondTurnCarriesNoFileBytesAtAll(t *testing.T) {
	d := newDoor(t)
	d.takesImages(t)

	att := d.attachOK(t, "diagram.png", onePixelPNG)
	encoded := base64.StdEncoding.EncodeToString(onePixelPNG)

	if rec := d.chat(t, "what is in this?", att.Path); rec.Code != http.StatusOK {
		t.Fatalf("chat with an attachment: %d %s", rec.Code, rec.Body.String())
	}
	if first := d.provider.call(t, 1).Raw; !strings.Contains(first, encoded) {
		t.Fatalf("the file's bytes are not in the request that was supposed to carry them; everything below would pass on a build that sends nothing:\n%s", first)
	}

	if rec := d.chat(t, "and what about now?"); rec.Code != http.StatusOK {
		t.Fatalf("second chat: %d %s", rec.Code, rec.Body.String())
	}
	second := d.provider.call(t, 2).Raw

	// The bytes themselves, then every wrapper either protocol has for bytes.
	// Naming the wrappers as well as the payload is what makes this survive a
	// future part kind: a file re-sent under a name nobody here anticipated
	// still arrives base64-encoded, because that is the only way either
	// protocol carries one.
	if strings.Contains(second, encoded) {
		t.Errorf("the file was re-encoded into the next turn; turn twenty of this conversation would pay for it twenty times")
	}
	for _, smuggle := range []string{";base64,", "image_url", "file_data", `"source"`} {
		if strings.Contains(second, smuggle) {
			t.Errorf("the later turn carries %q, so something file-shaped is being re-sent:\n%s", smuggle, second)
		}
	}
	// And what it does carry instead: the path, which is the whole of the
	// bargain — the model cannot look again, and it can still open the file.
	if !strings.Contains(second, att.Path) {
		t.Errorf("the later turn dropped the attachment entirely; the agent can no longer reach the file:\n%s", second)
	}
}

// Six files of six types on one message. Three of them no model can look
// inside — and all six are reachable by the agent at a workspace path, in the
// turn, through the same dispatch a model's tool call goes through.
func TestEveryAttachedTypeLandsAndTheAgentReachesItInATurn(t *testing.T) {
	d := newDoor(t)
	if _, err := d.cat.SetModelAccepts(context.Background(), d.modelID, []string{"image", "document"}); err != nil {
		t.Fatalf("declare what the model takes: %v", err)
	}

	files := []struct {
		name  string
		body  []byte
		shown bool
	}{
		{"notes.md", []byte("# what we agreed\n- the spreadsheet is authoritative\n"), true},
		{"diagram.png", onePixelPNG, true},
		{"report.pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n"), true},
		{"books.zip", append([]byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"), 0x00, 0xff, 0x7f), false},
		{"q3.xlsx", append([]byte("PK\x03\x04\x14\x00\x06\x00\x08\x00"), 0xde, 0xad, 0xbe, 0xef), false},
		{"standup.mp4", append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), 0x00, 0x01), false},
	}

	paths := make([]string, 0, len(files))
	byName := map[string]string{}
	for _, f := range files {
		att := d.attachOK(t, f.name, f.body)
		if shown := att.Kind != ""; shown != f.shown {
			t.Errorf("%q was answered as kind %q (skipped: %s); this test's premise is the opposite", f.name, att.Kind, att.Skipped)
		}
		// Byte identity on disk comes first: everything below is about a path,
		// and a path to a file that is not the file proves nothing.
		onDisk, err := os.ReadFile(filepath.Join(workdir.Dir(d.dir, d.wsID), filepath.FromSlash(att.Path)))
		if err != nil {
			t.Fatalf("%q did not land in the workspace: %v", f.name, err)
		}
		if !bytes.Equal(onDisk, f.body) {
			t.Fatalf("%q landed as %d bytes and was uploaded as %d", f.name, len(onDisk), len(f.body))
		}
		paths = append(paths, att.Path)
		byName[f.name] = att.Path
	}

	// The model reaches for three of them: the listing, the text file, and one
	// of the files it was refused. Three calls, then it stops.
	d.provider.answers(func(n int, call modelCall) modelReply {
		switch n {
		case 1:
			return asksFor(modelToolCall{ID: "c1", Name: "list_files", Args: `{"path":"attachments"}`})
		case 2:
			return asksFor(modelToolCall{ID: "c2", Name: "read_file", Args: `{"path":"` + byName["notes.md"] + `"}`})
		case 3:
			return asksFor(modelToolCall{ID: "c3", Name: "read_file", Args: `{"path":"` + byName["books.zip"] + `"}`})
		}
		return says("all six are here")
	})

	if rec := d.chat(t, "go through these", paths...); rec.Code != http.StatusOK {
		t.Fatalf("chat with six attachments: %d %s", rec.Code, rec.Body.String())
	}

	// What the model was shown, on the turn the files arrived on.
	first := d.provider.call(t, 1)
	if n := len(imageParts(first)); n != 1 {
		t.Errorf("the model was sent %d image parts, want 1 — only the PNG is an image", n)
	}
	if got := filePartFilenames(first); len(got) != 1 || got[0] != "report.pdf" {
		t.Errorf("the model was sent file parts %v, want just report.pdf", got)
	}
	prose := userProse(first)
	for _, f := range files {
		if !strings.Contains(prose, byName[f.name]) {
			t.Errorf("the turn never gives the path of %q, so the agent cannot reach it:\n%s", f.name, prose)
		}
	}
	if !strings.Contains(prose, "not shown to you") {
		t.Errorf("the model is not told that three of the six were withheld; it will answer as though it saw them:\n%s", prose)
	}

	// What the agent got when it reached for them. The results of all three
	// calls are in the last request, which is the conversation as the provider
	// finally saw it.
	last := d.provider.call(t, 4)
	results := last.toolResults()
	if len(results) != 3 {
		t.Fatalf("the agent's three tool calls came back as %d results: %v", len(results), results)
	}

	for _, f := range files {
		if !strings.Contains(results[0], byName[f.name]) {
			t.Errorf("%q is attached and invisible in the agent's own listing:\n%s", f.name, results[0])
		}
	}
	if !strings.Contains(results[1], "the spreadsheet is authoritative") {
		t.Errorf("the agent could not read the text it was sent:\n%s", results[1])
	}
	// The zip is refused by read_file, and the refusal has to be the routing:
	// it is the agent's only clue that there is another way into the file.
	if !strings.Contains(results[2], "_files") {
		t.Errorf("the agent is refused the zip without being told how to open it:\n%s", results[2])
	}
	if c := d.provider.complaints(); len(c) > 0 {
		t.Fatalf("the provider complained: %v", c)
	}
}

// The refusal an operator meets when a model was never declared able to take a
// picture ends: "if this model does take images — say so on the model in the
// catalog". This test does exactly that, through the API an operator has, and
// then sends the image again.
//
// It is here because the remedy was once unreachable. POST /api/v1/models
// dropped an accepts field on the way in (handleCreateModel's input struct had
// three fields and modality was not one of them), and there was no PATCH on a
// model at all — catalog.SetModelAccepts existed with nothing wired to it. So
// the refusal named a step that could not be taken, an operator who attached an
// image could only ever be told no, and the only way to declare a model's
// modality was to open the database and write the column by hand. Both halves
// are wired now: PATCH /api/v1/models/{id} answers, and a model can be added
// with its modality already declared.
//
// What this guards is the loop and not either route on its own — refused,
// declared through the operator's own API, sent. A route that answered 200 and
// recorded nothing would satisfy a test of the route and fail here, on the
// image that still does not go.
func TestAnOperatorCanSayInTheCatalogThatAModelTakesImages(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	// The premise: an image is refused, and the refusal sends the operator to
	// the catalog.
	att := d.attachOK(t, "shot.png", onePixelPNG)
	if rec := d.chat(t, "look at this", att.Path); rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	if n := d.provider.called(); n != 0 {
		t.Fatalf("the model was called %d times with a file it was never said to accept", n)
	}

	// The remedy, taken the way it is written: say so on the model.
	rec := d.request(t, http.MethodPatch, "/api/v1/models/"+id(d.modelID), d.adminTok, `{"accepts":["image"]}`)
	if rec.Code != http.StatusOK {
		t.Errorf("the operator was told to say in the catalog that this model takes images, and PATCH /api/v1/models/%d answered %d %s — there is no route that can say it",
			d.modelID, rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	// The same thing said the other way: declared when the model is added.
	model, err := d.cat.GetModel(ctx, d.modelID)
	if err != nil {
		t.Fatalf("read the model back: %v", err)
	}
	rec = d.request(t, http.MethodPost, "/api/v1/models", d.adminTok,
		`{"provider_id":`+id(model.ProviderID)+`,"model_name":"vision-model","label":"house / vision","accepts":["image"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("adding a model: %d %s", rec.Code, rec.Body.String())
	}
	var added struct {
		Accepts []string `json:"accepts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatalf("the answer is not a model: %s", rec.Body.String())
	}
	if len(added.Accepts) != 1 || added.Accepts[0] != "image" {
		t.Errorf("a model added as accepting images came back accepting %v — the field is dropped on the way in, so an operator cannot declare modality when adding a model either", added.Accepts)
	}

	// And having said it, the image goes.
	d.provider.reset()
	if rec := d.chat(t, "look at this", att.Path); rec.Code != http.StatusOK {
		t.Fatalf("chat after declaring the modality: %d %s", rec.Code, rec.Body.String())
	}
	if d.provider.called() == 0 {
		t.Fatal("the image is still refused after the operator did what the refusal told them to do")
	}
	if n := len(imageParts(d.provider.call(t, 1))); n != 1 {
		t.Errorf("the model was sent %d image parts after the modality was declared, want 1", n)
	}
}
