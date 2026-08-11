package server

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// Attaching a file to a message to the orchestrator.
//
// It is deliberately the same act as an inlet delivery, and it runs down the
// same path: the bytes land in the workspace's own directory as a file, and
// what travels onward is the PATH. The chat request that follows names those
// paths; the engine decides which of them a model can actually be shown. That
// split is the whole point — the operator can hand over anything at all, and
// an mp4 reaches the agent as something it can pass to a gear rather than as a
// refusal at the door.
//
// The upload is one file per request. An operator attaching five files makes
// five calls, which is what lets this reuse fileFromRequest exactly as the
// inlet uses it rather than growing a second reader that takes several parts —
// and a second reader of uploaded bodies is the one place where "the same, but
// slightly more permissive" would not be noticed.

// handleAttachFile lands one file in the workspace and answers with what will
// become of it: which path it is at, and whether the model will be shown it or
// the agent will be handed its path. The operator is told before they send,
// not after the answer comes back not mentioning their spreadsheet.
func (s *Server) handleAttachFile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	body, name, contentType, refusal := fileFromRequest(r)
	if refusal != nil {
		writeError(w, refusal.code, refusal.err.Error())
		return
	}

	rel := attachmentPath(name, contentType)
	if _, err := s.landFile(id, rel, body); err != nil {
		fail(w, r, err)
		return
	}
	slog.Info("attachment landed", "workspace_id", id, "path", rel, "bytes", len(body))

	// The engine, not this handler, says what a model can be shown — and it
	// says it by doing exactly what the turn will do. A cheaper answer here
	// (guess from the extension, say) would be a preview that disagrees with
	// the turn, which is worse than no preview.
	att, err := s.engine.DescribeAttachment(r.Context(), id, rel)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, att)
}

// attachmentPath is where an attached file lands: a directory of its own,
// named for the moment it arrived, holding the file under the name it came
// with.
//
// The uniqueness is in the directory rather than in the filename because the
// filename is read by people and by models — it is what a refusal quotes and
// what the composer shows — and "report.pdf" says something that
// "20260811-150405.881-3f9c1a04-report.pdf" does not. Unique it must be either
// way: an upload must never replace bytes an earlier message has already
// pointed an agent at.
func attachmentPath(name, contentType string) string {
	var salt [4]byte
	rand.Read(salt[:])
	landing := fmt.Sprintf("%s-%x", time.Now().UTC().Format("20060102-150405.000"), salt)
	return path.Join(workdir.AttachmentDir, landing, safeFileName(name, contentType))
}

// landFile writes bytes into a workspace's own directory and returns the
// absolute path they landed at.
//
// This is the one copy of that step. An inlet delivery and an operator's
// attachment are the same act — something from outside becoming a file inside
// the agent's reach — and two spellings of it would be two answers to "does
// this path stay inside the workspace", one of which would be the weaker.
func (s *Server) landFile(wsID int64, rel string, body []byte) (string, error) {
	full, err := workdir.ResolveInside(s.workspaceDir(wsID), rel)
	if err != nil {
		return "", fmt.Errorf("the file could not be placed in this workspace: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", fmt.Errorf("the workspace directory could not be prepared: %w", err)
	}
	if err := os.WriteFile(full, body, 0o600); err != nil {
		return "", fmt.Errorf("the file could not be written into this workspace: %w", err)
	}
	return full, nil
}
