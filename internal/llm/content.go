package llm

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The three kinds of content a model can be shown. There are three because
// three is all there is: a model reads text, looks at an image, and reads a
// PDF. It cannot look inside a zip, a spreadsheet or a video — those are
// opaque bytes to every model that exists, and this package refuses them here
// rather than letting a provider return a 400 the operator cannot act on.
const (
	PartText     = "text"
	PartImage    = "image"
	PartDocument = "document"
)

const (
	// MaxPartBytes is the largest single file that may be put in front of a
	// model, measured raw, before base64. It is the smaller of the two limits
	// the providers publish — Anthropic caps one image at 5 MB — applied to
	// documents as well, so there is one number for an operator to remember
	// instead of a table.
	//
	// It is deliberately far below the 32 MiB an inlet writes to disk
	// (inlet.MaxFilePayload): a file the disk can hold is not a file a model
	// can be asked to read. Anything larger goes the other route — the file
	// is already in the workspace, and a gear reads it from there.
	MaxPartBytes = 5 << 20

	// MaxRequestPartBytes bounds every attachment in one request together.
	// Base64 adds a third, so a request at this cap crosses the wire at about
	// 21 MiB and stays inside the 32 MB body both providers accept, with room
	// left for the conversation itself. A request over the cap is refused
	// before it is sent: a body the provider rejects at 30 MiB has already
	// cost the upload, and one it accepts has already cost the tokens.
	MaxRequestPartBytes = 16 << 20
)

// Part is one piece of a turn's content besides its prose: a file the model is
// being shown, or the text read out of one.
//
// Exactly one of Text and Data carries the content — Text for PartText, Data
// for PartImage and PartDocument. Data holds raw bytes and is base64-encoded
// once, at send time, in each provider's own idiom: keeping the bytes raw
// means one Part can be encoded for either provider and can be measured
// against the caps above without decoding anything.
type Part struct {
	Kind      string // PartText | PartImage | PartDocument
	Text      string // PartText: the file's text, verbatim
	MediaType string // PartImage, PartDocument: "image/png", "application/pdf"
	Data      []byte // PartImage, PartDocument: the file's bytes

	// Name is the file this part came from. It exists for the operator, who
	// needs a refusal that names which of five attachments was the problem,
	// and for the one provider field that wants a filename. It is never shown
	// to the model on its own: whether the model is told what it is looking
	// at is the caller's sentence to write, not this package's to invent.
	Name string
}

// TextPart is a plain block of text. It exists so a caller building a turn out
// of parts does not have to remember which field a text part fills.
func TextPart(text string) Part { return Part{Kind: PartText, Text: text} }

// FilePart turns a file into the part a model is shown, or refuses it with the
// sentence the operator needs. mediaType may be empty: a file that came off
// the disk or out of a gear often has no type attached, and then the bytes
// themselves are the only evidence there is.
func FilePart(name, mediaType string, data []byte) (Part, error) {
	kind, ok := ModelReadable(mediaType)
	if !ok {
		// The media type said nothing, or said something no provider takes.
		// Valid UTF-8 with no NUL byte is text whatever the header claimed —
		// that is a fact about these bytes, not a guess about the format, and
		// it is the difference between a .go file reaching the model and
		// being sent to a gear for no reason. A zip, an xlsx or an mp4 fails
		// this test within its first few bytes.
		if !isUTF8Text(data) {
			return Part{}, unreadableError(name, mediaType)
		}
		kind = PartText
	}
	p := Part{Kind: kind, Name: name, MediaType: baseMediaType(mediaType)}
	if kind == PartText {
		p.Text = string(data)
	} else {
		p.Data = data
	}
	if _, err := p.check(); err != nil {
		return Part{}, err
	}
	return p, nil
}

// ModelReadable reports the part kind a file of this media type would be sent
// as, and false when no model can look inside it. It is the single place the
// boundary is drawn: a caller routing a file asks here whether it goes to the
// model or to a gear, and the provider adapters refuse on the same answer.
func ModelReadable(mediaType string) (kind string, ok bool) {
	base := baseMediaType(mediaType)
	switch {
	case base == "":
		return "", false
	case base == "application/pdf":
		return PartDocument, true
	case strings.HasPrefix(base, "text/"), modelTextTypes[base]:
		return PartText, true
	case modelImageTypes[base]:
		return PartImage, true
	}
	return "", false
}

// modelImageTypes is every image format both adapters' providers document, and
// the two lists are the same four. An image/heic off a phone or an
// image/svg+xml is refused here, where the message can name the file, rather
// than by a provider whose 400 names none of them.
var modelImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// modelTextTypes are media types whose bytes are text but whose name does not
// begin with text/. The list is short on purpose: it holds what a workspace
// actually produces, and anything outside it still reaches the model as text
// when the bytes themselves prove they are UTF-8 (see FilePart).
var modelTextTypes = map[string]bool{
	"application/json":       true,
	"application/xml":        true,
	"application/yaml":       true,
	"application/x-yaml":     true,
	"application/toml":       true,
	"application/javascript": true,
	"application/x-ndjson":   true,
	"application/sql":        true,
}

// baseMediaType drops parameters and case, so "TEXT/CSV; charset=utf-8" and
// "text/csv" are the one media type they obviously are.
func baseMediaType(ct string) string {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return ""
	}
	if base, _, err := mime.ParseMediaType(ct); err == nil {
		return strings.ToLower(base)
	}
	// A malformed parameter is not a reason to lose the type: "image/png;"
	// still says what the file is, and refusing it would blame the operator
	// for whatever wrote the header.
	base, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// isUTF8Text reports whether these bytes are text a model can read: valid
// UTF-8 with no NUL byte, which every binary container has and no text file
// does.
func isUTF8Text(b []byte) bool {
	return utf8.Valid(b) && !bytes.ContainsRune(b, 0)
}

// check proves one part can be sent at all and returns the bytes it will count
// against the request cap.
func (p Part) check() (int, error) {
	switch p.Kind {
	case PartText:
		if p.Text == "" {
			return 0, emptyError(p.Name)
		}
		return len(p.Text), p.checkSize(len(p.Text))
	case PartImage, PartDocument:
		if len(p.Data) == 0 {
			return 0, emptyError(p.Name)
		}
		// The kind and the media type must agree. They can disagree only if a
		// caller filled the struct by hand, and a PDF announced as an image
		// would reach the provider as a corrupt image.
		if kind, ok := ModelReadable(p.MediaType); !ok || kind != p.Kind {
			return 0, unreadableError(p.Name, p.MediaType)
		}
		return len(p.Data), p.checkSize(len(p.Data))
	default:
		return 0, fmt.Errorf("%s was given content kind %q, which this build cannot send: a part is %q, %q or %q",
			describeFile(p.Name), p.Kind, PartText, PartImage, PartDocument)
	}
}

func (p Part) checkSize(n int) error {
	if n > MaxPartBytes {
		return fmt.Errorf("%s is %d bytes, larger than the %d bytes a model may be sent in one file. "+
			"The file is in the workspace: give its path to a gear, which reads it from disk and is not bound by this limit",
			describeFile(p.Name), n, MaxPartBytes)
	}
	return nil
}

// checkRequestParts refuses an oversized request before a byte of it is sent.
// Both adapters call it first, so the cap is one number enforced in one place
// no matter which provider the agent is bound to.
func checkRequestParts(turns []Turn) error {
	total := 0
	for _, t := range turns {
		for _, p := range t.Parts {
			n, err := p.check()
			if err != nil {
				return err
			}
			total += n
		}
	}
	if total > MaxRequestPartBytes {
		return fmt.Errorf("this request carries %d bytes of attachments, more than the %d bytes one model request may carry. "+
			"Send fewer files in one message, or give their paths to a gear — the files are in the workspace and a gear reads them from there",
			total, MaxRequestPartBytes)
	}
	return nil
}

// dataURL renders a part's bytes as the data: URL the OpenAI protocol carries
// files in.
func (p Part) dataURL() string {
	return "data:" + p.MediaType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
}

// fileName is what a provider field that insists on a filename gets. A part
// built from an upload has the real one; one built from raw bytes gets a name
// with the right extension, because a PDF called "attachment" has been
// rejected by servers that read the extension rather than the media type.
func (p Part) fileName() string {
	if clean := path.Base(strings.TrimSpace(p.Name)); clean != "" && clean != "." && clean != "/" {
		return clean
	}
	name := "attachment"
	if exts, _ := mime.ExtensionsByType(p.MediaType); len(exts) > 0 {
		name += exts[0]
	}
	return name
}

// unreadableError is the sentence an operator gets for a file no model can
// look inside. It names the route that does work, because "unsupported format"
// leaves them holding a file with no next step.
func unreadableError(name, mediaType string) error {
	what := fmt.Sprintf("%s has no media type and its bytes are not text", describeFile(name))
	if base := baseMediaType(mediaType); base != "" {
		what = fmt.Sprintf("%s is %s", describeFile(name), strconv.Quote(base))
	}
	return fmt.Errorf("%s, so no model can look inside it. A model can be sent text, an image (png, jpeg, gif or webp) and PDF. "+
		"The file is in the workspace: give its path to a gear that reads this format, and give the gear's output to the model", what)
}

func emptyError(name string) error {
	return fmt.Errorf("%s is empty, and a model cannot be shown an empty file. It is still in the workspace if the agent needs to say that it arrived", describeFile(name))
}

func describeFile(name string) string {
	if strings.TrimSpace(name) == "" {
		return "an attached file"
	}
	return strconv.Quote(name)
}
