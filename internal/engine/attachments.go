package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// Files the operator puts in front of the orchestrator.
//
// The shape of this is the whole design in one sentence: an attachment is a
// FILE IN THE WORKSPACE that a message points at, never bytes carried by the
// message. The HTTP layer lands it in the workspace directory the same way an
// inlet delivery lands there, and hands this package the path. From here the
// file goes two ways at once — its bytes to the model when the model can read
// them, and its path into the turn always, so the agent can name it in a gear
// for everything a model cannot look inside.
//
// Nothing is silently dropped and nothing is silently converted. A zip is
// still attached; the model is told it exists, where it is, and that it cannot
// be shown it.

// maxAttachmentsPerMessage bounds one message. The byte caps live in llm and
// are enforced there for what actually reaches a model, but a file the model
// is not shown still costs a line of prose, and a folder dragged onto the
// composer should be refused with a sentence rather than turned into four
// hundred of them.
const maxAttachmentsPerMessage = 20

// Attachment is one file a message put in front of an agent, as it is recorded
// on the timeline and as the operator is shown it.
//
// Kind is empty when the model was not shown the bytes, and Skipped then says
// why in the words the operator gets. Both are persisted rather than recomputed
// on display, because what the model was actually sent on the turn that
// happened is a fact about that turn: re-deriving it later would answer with
// today's limits and today's file, and the file may since have been replaced.
type Attachment struct {
	Path      string `json:"path"`       // workspace-relative, slash-separated
	MediaType string `json:"media_type"` // "" when nothing on this machine could name it
	Bytes     int64  `json:"bytes"`
	Kind      string `json:"kind,omitempty"`    // llm.PartText | PartImage | PartDocument
	Skipped   string `json:"skipped,omitempty"` // why the model was not shown it

	// Warning belongs to the preview and never to the timeline: it says this
	// file COULD be put in front of a model, but not in front of the one this
	// workspace's orchestrator is bound to — so the message would be refused.
	// It is answered while the operator is still holding the file, because
	// afterwards the same sentence arrives as a failed turn.
	Warning string `json:"warning,omitempty"`
}

// ToModel reports whether the bytes were put in front of the model.
func (a Attachment) ToModel() bool { return a.Kind != "" }

// DescribeAttachment answers "what will happen to this file" without sending
// anything, so the composer can tell the operator that the spreadsheet they
// just attached is going to their agents as a path before they hit send.
//
// It runs the same classification the turn runs — including reading the bytes,
// because whether a file with no extension is text is a fact about its bytes
// and not a guess from its name — so the preview and the turn cannot disagree.
func (e *Engine) DescribeAttachment(ctx context.Context, wsID int64, rel string) (Attachment, error) {
	att, part, err := e.classify(workdir.Dir(e.dataDir, wsID), rel, llm.MaxRequestPartBytes)
	if err != nil || part == nil {
		return att, err
	}
	att.Warning = e.modalityWarning(ctx, wsID, *part)
	return att, nil
}

// modalityWarning asks the catalog whether the model this workspace answers
// with was declared able to take this kind of file, and returns the refusal
// the operator would otherwise meet as a failed turn.
//
// Every lookup failure here yields no warning rather than an error: this is a
// preview, the turn itself checks again with the model it is actually about to
// call, and a workspace whose orchestrator has no model has a louder problem
// than an unwarned attachment.
func (e *Engine) modalityWarning(ctx context.Context, wsID int64, part llm.Part) string {
	orch, err := e.orchestrator(ctx, wsID)
	if err != nil || orch.ModelID == nil {
		return ""
	}
	model, err := e.cat.GetModel(ctx, *orch.ModelID)
	if err != nil {
		slog.Warn("could not check what the orchestrator's model accepts", "workspace_id", wsID, "err", err)
		return ""
	}
	if err := e.cat.CheckAccepts(ctx, model, []llm.Part{part}); err != nil {
		return err.Error()
	}
	return ""
}

// collectAttachments turns the paths a message named into the record that goes
// on the timeline and the parts that go to the model.
//
// A path that names nothing is an error for the whole message rather than a
// note on it: the operator asked for a file to be in front of the agent, and
// answering the message without it would answer a question they did not ask.
// A file the model cannot READ is the opposite — that is an ordinary outcome
// with a route of its own, and it costs the message nothing.
func (e *Engine) collectAttachments(wsID int64, paths []string) ([]Attachment, []llm.Part, error) {
	if len(paths) == 0 {
		return nil, nil, nil
	}
	if len(paths) > maxAttachmentsPerMessage {
		return nil, nil, fmt.Errorf("%d files were attached to one message and %d is the limit; "+
			"send them in several messages, or put them in the workspace and tell the agent which directory to work through",
			len(paths), maxAttachmentsPerMessage)
	}

	root := workdir.Dir(e.dataDir, wsID)
	// One budget for the whole message, spent in the order the operator
	// attached them. What does not fit is not an error: the file is in the
	// workspace, so it goes the way every unreadable file goes.
	budget := int64(llm.MaxRequestPartBytes)

	atts := make([]Attachment, 0, len(paths))
	var parts []llm.Part
	for _, p := range paths {
		att, part, err := e.classify(root, p, budget)
		if err != nil {
			return nil, nil, err
		}
		if part != nil {
			budget -= partBytes(*part)
			parts = append(parts, *part)
		}
		atts = append(atts, att)
	}
	return atts, parts, nil
}

// classify decides what becomes of one attached file, and is the only place
// that decides it. budget is how many more bytes this message may still put in
// front of the model.
func (e *Engine) classify(root, rel string, budget int64) (Attachment, *llm.Part, error) {
	clean := workdir.Clean(rel)
	full, err := workdir.ResolveInside(root, rel)
	if err != nil {
		return Attachment{}, nil, fmt.Errorf("%q cannot be attached: %w", clean, err)
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return Attachment{}, nil, fmt.Errorf("there is no %q in this workspace, so it cannot be attached — "+
				"attach the file again, or name a path the Files panel shows", clean)
		}
		return Attachment{}, nil, err
	}
	if info.IsDir() {
		return Attachment{}, nil, fmt.Errorf("%q is a directory, not a file — attach the files in it, "+
			"or tell the agent the directory and let it list what is there", clean)
	}
	if !info.Mode().IsRegular() {
		return Attachment{}, nil, fmt.Errorf("%q is not an ordinary file, so there is nothing to attach", clean)
	}

	att := Attachment{Path: clean, MediaType: mediaTypeOf(clean), Bytes: info.Size()}
	switch {
	case info.Size() == 0:
		att.Skipped = "it is empty, and a model cannot be shown an empty file"
		return att, nil, nil
	case info.Size() > llm.MaxPartBytes:
		// Checked before the read on purpose: a 30 MB file that is going to a
		// gear anyway should not be pulled into this process to be told so.
		att.Skipped = fmt.Sprintf("it is %d bytes, and %d is the most a model may be shown in one file — "+
			"a gear reads it from the workspace instead, where there is no such limit", info.Size(), llm.MaxPartBytes)
		return att, nil, nil
	case info.Size() > budget:
		att.Skipped = fmt.Sprintf("this message already carries the %d bytes of files one model request may — "+
			"a gear reads this one from the workspace instead", llm.MaxRequestPartBytes)
		return att, nil, nil
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return Attachment{}, nil, err
	}
	// llm decides what a model can be shown, and its refusal is carried through
	// word for word: it names the file, says why, and says to hand the path to
	// a gear — which is exactly what the agent reading this note has to do.
	part, err := llm.FilePart(path.Base(clean), att.MediaType, data)
	if err != nil {
		att.Skipped = err.Error()
		return att, nil, nil
	}
	// A text file arrives as text, which on its own is prose with nothing
	// saying which of three attachments it came from. The path goes in front of
	// it — here rather than in llm, which is told what to send and not what
	// this workspace calls it. An image needs no such line: the note above the
	// turn lists them in the order they are shown.
	if part.Kind == llm.PartText {
		if label := "--- " + clean + " ---\n"; len(label)+len(part.Text) <= llm.MaxPartBytes {
			part.Text = label + part.Text
		}
		// A file within a few bytes of the cap keeps its content and loses the
		// label. The file is what was asked for.
	}
	att.Kind = part.Kind
	return att, &part, nil
}

// partBytes is what one part will count against the request budget: the bytes
// as they are here, before base64, which is how llm measures them too.
func partBytes(p llm.Part) int64 {
	if p.Kind == llm.PartText {
		return int64(len(p.Text))
	}
	return int64(len(p.Data))
}

// mediaTypeOf names a file from its extension, and answers "" when nothing on
// this machine can. The header a browser sent at upload time is deliberately
// not consulted: an attachment is a file in the workspace that a later message
// points at, so what it is has to be readable off the file itself, the same
// answer whether it arrived through the composer, through an inlet, or from a
// gear that wrote it. llm.FilePart reads the bytes when this comes back empty.
func mediaTypeOf(clean string) string {
	ct := mime.TypeByExtension(path.Ext(clean))
	if ct == "" {
		return ""
	}
	if base, _, err := mime.ParseMediaType(ct); err == nil {
		return base
	}
	return ct
}

// attachmentNote is what the model is told about the files on a message. It is
// generated from the record rather than written into the message content, so
// the live turn and every replay of it say the same thing — see buildHistory,
// where the same call rebuilds it without the bytes.
//
// The paths are the point. A model that cannot see a spreadsheet can still be
// useful about it if it knows the file exists, where it is, and that a gear
// opens it; without the path it can only apologise.
func attachmentNote(atts []Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	noun := "file"
	if len(atts) > 1 {
		noun = "files"
	}
	// The last sentence is what makes the replay decision legible to the model
	// rather than surprising: it is told, at the moment it is being shown a
	// file, that later turns will carry only this note. Otherwise a model
	// reading the note three turns on would take "shown to you" for a promise
	// about the request in front of it.
	b := fmt.Appendf(nil, "[%d %s attached to this message by the operator. Each one is in this workspace at the "+
		"path given — including any you were not shown, which is why the path is here: read it with read_file if "+
		"it is text, or name it in a gear's %s argument for anything else. A file is put in front of you on the "+
		"message it arrives with; further on in this conversation only this note remains, so open it from the "+
		"workspace if you need it again.]\n", len(atts), noun, gearFilesArg)
	for _, a := range atts {
		kind := a.MediaType
		if kind == "" {
			kind = "type unknown"
		}
		b = fmt.Appendf(b, "- %s — %s, %d bytes — ", a.Path, kind, a.Bytes)
		if a.ToModel() {
			b = fmt.Appendf(b, "shown to you in this message, as %s.\n", a.Kind)
			continue
		}
		b = fmt.Appendf(b, "not shown to you: %s\n", a.Skipped)
	}
	return strings.TrimRight(string(b), "\n")
}

// withAttachments is the operator's own words plus that note. An empty message
// with a file on it is an ordinary thing to send — "here" and a photograph —
// so the note stands alone rather than the turn being dropped for having no
// text.
func withAttachments(text string, atts []Attachment) string {
	note := attachmentNote(atts)
	switch {
	case note == "":
		return text
	case strings.TrimSpace(text) == "":
		return note
	}
	return text + "\n\n" + note
}

// attachedParts is every file a conversation is about to put in front of the
// model. In practice that is one turn's worth — replay carries paths, not
// bytes — but it reads the whole conversation rather than assuming that, so
// the check cannot be walked past by a future caller that builds history some
// other way.
func attachedParts(history []llm.Turn) []llm.Part {
	var parts []llm.Part
	for _, t := range history {
		parts = append(parts, t.Parts...)
	}
	return parts
}

// attachmentsOf reads a timeline row's attachments back out of its meta. A row
// written before this existed, or by anything that did not attach files, has
// none — and a meta that cannot be parsed is logged and treated as none,
// because a replay that fails outright would take the whole conversation with
// it over a display field.
func attachmentsOf(meta string) []Attachment {
	if meta == "" || meta == "{}" {
		return nil
	}
	var m struct {
		Attachments []Attachment `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		slog.Warn("bad attachment meta in timeline", "err", err)
		return nil
	}
	return m.Attachments
}

// attachmentMeta is the other direction, for the row being written.
func attachmentMeta(atts []Attachment) (string, error) {
	if len(atts) == 0 {
		// The empty meta AppendMessage already writes for every other message,
		// so a message with no files on it is byte-for-byte the row it was
		// before attachments existed.
		return "", nil
	}
	raw, err := json.Marshal(map[string]any{"attachments": atts})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
