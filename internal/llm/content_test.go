package llm

// What actually crosses the wire when a message carries a file.
//
// Every assertion here is on the request body a provider received over a real
// socket. That is the only place the truth about a wire format lives: a test of
// anthropicBlock in isolation agrees with itself while the adapter that calls
// it drops the block, and a provider's 400 arrives after the upload, after the
// bill, and names neither the file nor the turn. The server below is a loopback
// httptest server answering a real stream — no key, no provider, nothing off
// this machine.
//
// The refusal cases assert the request COUNT as well as the error, because
// "refused" and "refused before it was sent" are different promises and only
// the second one is worth anything to an operator's bill.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// --- the files ------------------------------------------------------------
//
// Real formats, not invented byte strings: the whole path exists to carry a
// file unchanged, and bytes chosen to be convenient would prove nothing about
// bytes that are not.

// pngOnePixel is a complete 68-byte PNG of one transparent pixel.
var pngOnePixel = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// gifOnePixel is a complete 43-byte GIF89a. A second image format is here for
// one reason: it is what catches an adapter that hardcodes "image/png" and
// still passes every test written with a PNG.
var gifOnePixel = []byte{
	'G', 'I', 'F', '8', '9', 'a', 0x01, 0x00, 0x01, 0x00,
	0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff,
	0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00,
	0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00,
	0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
}

// samplePDF carries the header, the binary comment a real PDF writer emits so
// that transfer software stops treating the file as text, and a trailer. Those
// four comment bytes are not valid UTF-8, which is the property that matters
// here: what survives the encoding has to be bytes, not characters.
var samplePDF = []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")

// A zip, a spreadsheet and a video: the three shapes of "a file that arrived
// and no model can look inside it". The xlsx opens with the same four bytes as
// the zip because it is one, which is the point — the media type is what tells
// them apart and neither answer is "show it to the model".
var (
	sampleZip  = append([]byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"), bytes.Repeat([]byte{0x00, 0xff, 0x7f}, 32)...)
	sampleXLSX = append([]byte("PK\x03\x04\x14\x00\x06\x00\x08\x00"), bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 24)...)
	sampleMP4  = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), bytes.Repeat([]byte{0x00, 0x01}, 32)...)
)

// --- the provider on the other end ----------------------------------------

// wire is a provider listening on a real socket that keeps every request body
// it was sent, and counts them. Both protocols are served from the same server
// on their own paths, so one fixture answers whichever adapter a test builds.
type wire struct {
	srv *httptest.Server

	mu     sync.Mutex
	bodies [][]byte
}

const anthropicSeenStream = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"seen"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`

const openAISeenStream = `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"seen"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":3}}

data: [DONE]

`

func newWire(t *testing.T) *wire {
	t.Helper()
	w := &wire{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", w.answer(anthropicSeenStream))
	mux.HandleFunc("POST /v1/chat/completions", w.answer(openAISeenStream))
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wire) answer(stream string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, "could not read the request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.bodies = append(w.bodies, body)
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(rw, stream)
	}
}

// hits is how many requests actually left the adapter. A refusal that costs a
// request is not the refusal this package promises.
func (w *wire) hits() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.bodies)
}

func (w *wire) body(t *testing.T, n int) []byte {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if n < 1 || n > len(w.bodies) {
		t.Fatalf("the provider was sent %d requests; there is no request %d", len(w.bodies), n)
	}
	return w.bodies[n-1]
}

// chat runs one turn against whichever protocol, and returns the adapter's
// error rather than failing on it: half the cases here are about the error.
func (w *wire) chat(t *testing.T, providerType string, r Request) error {
	t.Helper()
	base := w.srv.URL
	if providerType == TypeOpenAICompatible {
		// The base URL an operator enters for an OpenAI-compatible server
		// carries the version segment; the Anthropic adapter adds its own.
		base += "/v1"
	}
	c, err := New(providerType, base, "test-key")
	if err != nil {
		t.Fatalf("New(%q): %v", providerType, err)
	}
	_, err = c.Chat(context.Background(), r, nil)
	return err
}

// sent runs one turn that is expected to succeed and returns the one body the
// provider received, decoded.
func (w *wire) sent(t *testing.T, providerType string, r Request) map[string]any {
	t.Helper()
	if err := w.chat(t, providerType, r); err != nil {
		t.Fatalf("%s chat: %v", providerType, err)
	}
	if n := w.hits(); n != 1 {
		t.Fatalf("%s: the adapter made %d requests for one turn, want exactly 1", providerType, n)
	}
	var m map[string]any
	if err := json.Unmarshal(w.body(t, 1), &m); err != nil {
		t.Fatalf("%s: the adapter sent a body that is not JSON: %v\n%s", providerType, err, w.body(t, 1))
	}
	return m
}

// --- reading a recorded body ----------------------------------------------

func messagesOf(t *testing.T, sent map[string]any) []map[string]any {
	t.Helper()
	raw, ok := sent["messages"].([]any)
	if !ok {
		t.Fatalf("the request carries no messages array: %v", sent["messages"])
	}
	out := make([]map[string]any, 0, len(raw))
	for i, m := range raw {
		msg, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("message %d is %T, not an object", i, m)
		}
		out = append(out, msg)
	}
	return out
}

// theUserTurn returns the one message whose role is user. Every test below
// sends exactly one, so more than one means the adapter split a turn.
func theUserTurn(t *testing.T, sent map[string]any) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, m := range messagesOf(t, sent) {
		if m["role"] == "user" {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the request carries %d user messages, want 1: %v", len(found), sent["messages"])
	}
	return found[0]
}

// blocksOf insists a turn's content is an array of objects. A turn carrying a
// file has no other legal shape in either protocol, and a string here means the
// file never made it into the request at all.
func blocksOf(t *testing.T, msg map[string]any) []map[string]any {
	t.Helper()
	raw, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("a turn carrying a file was sent with content %#v — the file is not in the request", msg["content"])
	}
	out := make([]map[string]any, 0, len(raw))
	for i, b := range raw {
		block, ok := b.(map[string]any)
		if !ok {
			t.Fatalf("content block %d is %T, not an object", i, b)
		}
		out = append(out, block)
	}
	return out
}

func blockOfType(t *testing.T, blocks []map[string]any, want string) map[string]any {
	t.Helper()
	for _, b := range blocks {
		if b["type"] == want {
			return b
		}
	}
	kinds := make([]string, 0, len(blocks))
	for _, b := range blocks {
		kinds = append(kinds, fmt.Sprint(b["type"]))
	}
	t.Fatalf("no %q block in the turn the provider received; it carried %v", want, kinds)
	return nil
}

func sub(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q is %#v, not an object", key, m[key])
	}
	return v
}

func field(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("field %q is %#v, not a string", key, m[key])
	}
	return v
}

// decoded is the file as the provider would reconstruct it. Comparing this to
// the bytes that went in is the only assertion that proves a file was CARRIED
// rather than merely mentioned.
func decoded(t *testing.T, b64 string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("the provider was sent something that is not base64: %v (%.60s…)", err, b64)
	}
	return raw
}

func fromDataURL(t *testing.T, url, wantPrefix string) []byte {
	t.Helper()
	if !strings.HasPrefix(url, wantPrefix) {
		t.Fatalf("the file did not arrive as %s… — it arrived as %.60s…", wantPrefix, url)
	}
	return decoded(t, strings.TrimPrefix(url, wantPrefix))
}

func sameBytes(t *testing.T, got, want []byte, what string) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: the provider received %d bytes and the file is %d — what reaches the model is not the file", what, len(got), len(want))
	}
}

// --- 1. a text-only turn is what it always was ----------------------------

// The exact bodies both adapters produced before Part existed, recorded from a
// conversation with every shape in it: prose, a tool call, a tool result, and
// an answer. They are literals rather than a golden file because the promise is
// specific and small — nothing about a message without files changed — and a
// promise stored next to the code that would break it is one somebody re-records
// instead of reading.
//
// The two things to look at if this ever fails. In the Anthropic body, content
// is an array of blocks and always was. In the OpenAI body, content is a PLAIN
// STRING at every turn — not an array of one text part — and the assistant turn
// that only calls a tool has no content field at all. Local OpenAI-compatible
// servers reject both of the alternatives, and an install that worked yesterday
// would stop working with no change an operator can see.
const anthropicTextOnlyBody = `{"max_tokens":1024,"messages":[{"content":[{"text":"what is in notes.md?","type":"text"}],"role":"user"},{"content":[{"id":"call_1","input":{"path":"notes.md"},"name":"read_file","type":"tool_use"}],"role":"assistant"},{"content":[{"content":"# what we agreed","is_error":false,"tool_use_id":"call_1","type":"tool_result"}],"role":"user"},{"content":[{"text":"It is the note we agreed.","type":"text"}],"role":"assistant"},{"content":[{"text":"thanks","type":"text"}],"role":"user"}],"model":"a-model","stream":true,"system":"You are the orchestrator.","tools":[{"description":"Read a text file from your workspace.","input_schema":{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"},"name":"read_file"}]}`

const openAITextOnlyBody = `{"max_tokens":1024,"messages":[{"content":"You are the orchestrator.","role":"system"},{"content":"what is in notes.md?","role":"user"},{"role":"assistant","tool_calls":[{"function":{"arguments":"{\"path\":\"notes.md\"}","name":"read_file"},"id":"call_1","type":"function"}]},{"content":"# what we agreed","role":"tool","tool_call_id":"call_1"},{"content":"It is the note we agreed.","role":"assistant"},{"content":"thanks","role":"user"}],"model":"a-model","stream":true,"stream_options":{"include_usage":true},"tools":[{"function":{"description":"Read a text file from your workspace.","name":"read_file","parameters":{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}},"type":"function"}]}`

// conversationWithoutFiles is the request both goldens were recorded from.
func conversationWithoutFiles() Request {
	return Request{
		Model:     "a-model",
		System:    "You are the orchestrator.",
		MaxTokens: 1024,
		Tools: []Tool{{
			Name:        "read_file",
			Description: "Read a text file from your workspace.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			},
		}},
		Messages: []Turn{
			{Role: "user", Text: "what is in notes.md?"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", InputJSON: `{"path":"notes.md"}`}}},
			{Role: "user", ToolResults: []ToolResult{{CallID: "call_1", Name: "read_file", Content: "# what we agreed"}}},
			{Role: "assistant", Text: "It is the note we agreed."},
			{Role: "user", Text: "thanks"},
		},
	}
}

func TestAConversationWithoutFilesCrossesTheWireByteForByte(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		providerType string
		golden       string
	}{
		{TypeAnthropic, anthropicTextOnlyBody},
		{TypeOpenAICompatible, openAITextOnlyBody},
	} {
		t.Run(tc.providerType, func(t *testing.T) {
			t.Parallel()
			w := newWire(t)
			if err := w.chat(t, tc.providerType, conversationWithoutFiles()); err != nil {
				t.Fatalf("chat: %v", err)
			}
			got := string(w.body(t, 1))
			if got != tc.golden {
				t.Fatalf("a message with no files on it no longer crosses the wire as it did.\n got: %s\nwant: %s", got, tc.golden)
			}
		})
	}
}

// --- 2. an image reaches both providers as an image -----------------------

func TestAnthropicReceivesAnImageAsABase64Block(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		mediaType string
		data      []byte
	}{
		{"shot.png", "image/png", pngOnePixel},
		{"badge.gif", "image/gif", gifOnePixel},
	} {
		t.Run(tc.mediaType, func(t *testing.T) {
			t.Parallel()
			w := newWire(t)
			part, err := FilePart(tc.name, tc.mediaType, tc.data)
			if err != nil {
				t.Fatalf("FilePart(%s): %v", tc.mediaType, err)
			}
			sent := w.sent(t, TypeAnthropic, Request{
				Model:    "a-model",
				Messages: []Turn{{Role: "user", Text: "what is in this?", Parts: []Part{part}}},
			})

			blocks := blocksOf(t, theUserTurn(t, sent))
			img := blockOfType(t, blocks, "image")
			source := sub(t, img, "source")
			if got := field(t, source, "type"); got != "base64" {
				t.Errorf("the image source type is %q, want %q — the API cannot fetch a file off this machine", got, "base64")
			}
			if got := field(t, source, "media_type"); got != tc.mediaType {
				t.Errorf("the image was announced as %q, want %q: the provider decodes it as the wrong format", got, tc.mediaType)
			}
			sameBytes(t, decoded(t, field(t, source, "data")), tc.data, "the image block")

			// The prose is still there, and it is after the picture — a question
			// about an image the model has not reached yet is answered worse.
			text := blockOfType(t, blocks, "text")
			if field(t, text, "text") != "what is in this?" {
				t.Errorf("the operator's question did not survive the file: %q", text["text"])
			}
			if blocks[0]["type"] != "image" {
				t.Errorf("the question was placed ahead of the image: the blocks arrived as %v", blocks)
			}
		})
	}
}

func TestOpenAIReceivesAnImageAsADataURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		mediaType string
		data      []byte
	}{
		{"shot.png", "image/png", pngOnePixel},
		{"badge.gif", "image/gif", gifOnePixel},
	} {
		t.Run(tc.mediaType, func(t *testing.T) {
			t.Parallel()
			w := newWire(t)
			part, err := FilePart(tc.name, tc.mediaType, tc.data)
			if err != nil {
				t.Fatalf("FilePart(%s): %v", tc.mediaType, err)
			}
			sent := w.sent(t, TypeOpenAICompatible, Request{
				Model:    "a-model",
				Messages: []Turn{{Role: "user", Text: "what is in this?", Parts: []Part{part}}},
			})

			blocks := blocksOf(t, theUserTurn(t, sent))
			img := blockOfType(t, blocks, "image_url")
			url := field(t, sub(t, img, "image_url"), "url")
			sameBytes(t, fromDataURL(t, url, "data:"+tc.mediaType+";base64,"), tc.data, "the image_url part")

			text := blockOfType(t, blocks, "text")
			if field(t, text, "text") != "what is in this?" {
				t.Errorf("the operator's question did not survive the file: %q", text["text"])
			}
			if blocks[0]["type"] != "image_url" {
				t.Errorf("the question was placed ahead of the image: the parts arrived as %v", blocks)
			}
		})
	}
}

// A turn with a file is an array; every OTHER turn of the same request is still
// the plain string it was. Widening the shape of one turn must not widen the
// shape of the conversation around it — that is the regression that would take
// a local server down while the image test kept passing.
func TestOnlyTheTurnCarryingTheFileChangesShape(t *testing.T) {
	t.Parallel()

	w := newWire(t)
	part, err := FilePart("shot.png", "image/png", pngOnePixel)
	if err != nil {
		t.Fatalf("FilePart: %v", err)
	}
	sent := w.sent(t, TypeOpenAICompatible, Request{
		Model: "a-model",
		Messages: []Turn{
			{Role: "user", Text: "here is the diagram", Parts: []Part{part}},
			{Role: "assistant", Text: "I see it."},
			{Role: "user", Text: "and now?"},
		},
	})

	msgs := messagesOf(t, sent)
	if len(msgs) != 3 {
		t.Fatalf("three turns went in and %d came out: %v", len(msgs), msgs)
	}
	if _, ok := msgs[0]["content"].([]any); !ok {
		t.Fatalf("the turn carrying the image was sent as %#v — the file is not in the request", msgs[0]["content"])
	}
	for i, m := range msgs[1:] {
		if _, ok := m["content"].(string); !ok {
			t.Errorf("turn %d carries no file and crossed the wire as %T instead of a plain string", i+2, m["content"])
		}
	}
}

// --- 3. a PDF reaches both providers as a document ------------------------

func TestAPDFReachesBothProvidersAsADocument(t *testing.T) {
	t.Parallel()

	t.Run(TypeAnthropic, func(t *testing.T) {
		t.Parallel()
		w := newWire(t)
		part, err := FilePart("report.pdf", "application/pdf", samplePDF)
		if err != nil {
			t.Fatalf("FilePart(pdf): %v", err)
		}
		sent := w.sent(t, TypeAnthropic, Request{
			Model:    "a-model",
			Messages: []Turn{{Role: "user", Text: "summarise this", Parts: []Part{part}}},
		})

		blocks := blocksOf(t, theUserTurn(t, sent))
		doc := blockOfType(t, blocks, "document")
		source := sub(t, doc, "source")
		if got := field(t, source, "media_type"); got != "application/pdf" {
			t.Errorf("the PDF was announced as %q, want application/pdf", got)
		}
		if got := field(t, source, "type"); got != "base64" {
			t.Errorf("the document source type is %q, want base64", got)
		}
		sameBytes(t, decoded(t, field(t, source, "data")), samplePDF, "the document block")
	})

	// A PDF is NOT an image_url on this protocol, whatever the shape of the
	// image case above suggests: chat-completions carries a document as a file
	// part with its own filename, and a server that implements one does not
	// thereby implement the other.
	t.Run(TypeOpenAICompatible, func(t *testing.T) {
		t.Parallel()
		w := newWire(t)
		part, err := FilePart("report.pdf", "application/pdf", samplePDF)
		if err != nil {
			t.Fatalf("FilePart(pdf): %v", err)
		}
		sent := w.sent(t, TypeOpenAICompatible, Request{
			Model:    "a-model",
			Messages: []Turn{{Role: "user", Text: "summarise this", Parts: []Part{part}}},
		})

		blocks := blocksOf(t, theUserTurn(t, sent))
		if len(imageURLs(blocks)) > 0 {
			t.Fatalf("the PDF was sent as an image part; a provider decoding it as an image gets nothing readable")
		}
		file := sub(t, blockOfType(t, blocks, "file"), "file")
		if got := field(t, file, "filename"); got != "report.pdf" {
			t.Errorf("the file part is named %q, want %q — servers that read the extension refuse the rest", got, "report.pdf")
		}
		sameBytes(t, fromDataURL(t, field(t, file, "file_data"), "data:application/pdf;base64,"), samplePDF, "the file part")
	})
}

func imageURLs(blocks []map[string]any) []string {
	var out []string
	for _, b := range blocks {
		if b["type"] != "image_url" {
			continue
		}
		if img, ok := b["image_url"].(map[string]any); ok {
			if url, ok := img["url"].(string); ok {
				out = append(out, url)
			}
		}
	}
	return out
}

// --- 4. what no model can look inside is refused, and refused early -------

// The refusal is the product here, not the error value: an operator holding a
// spreadsheet needs to be told which file, that no model can read it, and what
// to do with it instead. "unsupported media type" is none of those.
func TestFilesNoModelCanReadAreRefusedWithSomewhereToGo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		mediaType string
		data      []byte
	}{
		{"books.zip", "application/zip", sampleZip},
		{"q3.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", sampleXLSX},
		{"standup.mp4", "video/mp4", sampleMP4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			part, err := FilePart(tc.name, tc.mediaType, tc.data)
			if err == nil {
				t.Fatalf("%q was accepted as a %s part; no model can look inside it", tc.name, part.Kind)
			}
			msg := err.Error()
			for _, want := range []string{tc.name, tc.mediaType, "gear", "workspace"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the refusal never says %q, so the operator cannot act on it: %s", want, msg)
				}
			}
			// What a model CAN be shown, named in the refusal, because the next
			// question after "no" is always "then what".
			for _, want := range []string{"text", "image", "png", "PDF"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the refusal does not say a model can be sent %q: %s", want, msg)
				}
			}
		})
	}
}

// The same three formats, hand-built into a Part the way a caller that skipped
// FilePart would build them, and sent. The adapters must refuse before the
// socket is used: a provider's own 400 costs the upload and names neither the
// file nor the turn it was on.
func TestUnreadableFilesNeverReachTheProvider(t *testing.T) {
	t.Parallel()

	for _, providerType := range []string{TypeAnthropic, TypeOpenAICompatible} {
		t.Run(providerType, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name      string
				kind      string
				mediaType string
				data      []byte
			}{
				{"books.zip", PartDocument, "application/zip", sampleZip},
				{"q3.xlsx", PartDocument, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", sampleXLSX},
				{"standup.mp4", PartImage, "video/mp4", sampleMP4},
			} {
				w := newWire(t)
				err := w.chat(t, providerType, Request{
					Model: "a-model",
					Messages: []Turn{{Role: "user", Text: "what is in this?", Parts: []Part{
						{Kind: tc.kind, Name: tc.name, MediaType: tc.mediaType, Data: tc.data},
					}}},
				})
				if err == nil {
					t.Errorf("%s: %q was sent to the provider as a %s part", providerType, tc.name, tc.kind)
				} else if !strings.Contains(err.Error(), tc.name) {
					t.Errorf("%s: the refusal does not name the file: %v", providerType, err)
				}
				if n := w.hits(); n != 0 {
					t.Errorf("%s: %d requests were made carrying %q before it was refused", providerType, n, tc.name)
				}
			}
		})
	}
}

// --- 6. the size caps are refusals, not post-mortems ----------------------

// One file over the per-file cap. The refusal has to arrive before the bytes
// do: a 5 MB image base64s to nearly 7 MB, and a provider that rejects it has
// already been sent all of it.
func TestAFileOverTheCapNeverReachesTheProvider(t *testing.T) {
	t.Parallel()

	oversize := bytes.Repeat([]byte{0x89}, MaxPartBytes+1)

	// Through the front door first: the file never becomes a Part at all.
	// Errorf rather than Fatalf, so that the wire cases below still run and say
	// whether the bytes also left the machine.
	if _, err := FilePart("huge.png", "image/png", oversize); err == nil {
		t.Errorf("a file over the per-file cap was accepted as a part")
	} else {
		msg := err.Error()
		for _, want := range []string{"huge.png", fmt.Sprint(MaxPartBytes), "gear", "workspace"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal never says %q: %s", want, msg)
			}
		}
	}

	for _, providerType := range []string{TypeAnthropic, TypeOpenAICompatible} {
		t.Run(providerType, func(t *testing.T) {
			t.Parallel()
			w := newWire(t)
			err := w.chat(t, providerType, Request{
				Model: "a-model",
				Messages: []Turn{{Role: "user", Text: "look at this", Parts: []Part{
					{Kind: PartImage, Name: "huge.png", MediaType: "image/png", Data: oversize},
				}}},
			})
			if err == nil {
				t.Errorf("a %d-byte file was sent to the provider", len(oversize))
			}
			if n := w.hits(); n != 0 {
				t.Errorf("%d requests were made carrying a file over the cap before it was refused", n)
			}
		})
	}
}

// Several files that each fit, together over what one request may carry. The
// budget is the whole request's, so no single part can be blamed and the check
// has to run across the conversation.
func TestARequestOverTheTotalCapNeverReachesTheProvider(t *testing.T) {
	t.Parallel()

	// Four files of exactly the per-file cap: every one of them legal alone,
	// and 20 MiB together against a 16 MiB request.
	var turns []Turn
	for i := 0; i < (MaxRequestPartBytes/MaxPartBytes)+1; i++ {
		turns = append(turns, Turn{
			Role: "user",
			Text: fmt.Sprintf("file %d", i),
			Parts: []Part{{
				Kind: PartImage, Name: fmt.Sprintf("page-%d.png", i),
				MediaType: "image/png", Data: bytes.Repeat([]byte{0x89}, MaxPartBytes),
			}},
		})
	}

	for _, providerType := range []string{TypeAnthropic, TypeOpenAICompatible} {
		t.Run(providerType, func(t *testing.T) {
			w := newWire(t)
			err := w.chat(t, providerType, Request{Model: "a-model", Messages: turns})
			if err == nil {
				t.Fatalf("a request carrying %d bytes of files was sent; the cap is %d",
					len(turns)*MaxPartBytes, MaxRequestPartBytes)
			}
			if !strings.Contains(err.Error(), fmt.Sprint(MaxRequestPartBytes)) {
				t.Errorf("the refusal does not say what the limit is: %v", err)
			}
			if n := w.hits(); n != 0 {
				t.Errorf("%d requests were made carrying %d bytes before the cap was noticed", n, len(turns)*MaxPartBytes)
			}
		})
	}
}
