// Package workdir owns the workspace's own directory on disk: where it is,
// what lives inside it by convention, and the single proof that a path stays
// within it.
//
// It exists because three packages now need that proof and only one of them may
// own it. The proof started life in the HTTP layer, as server.resolveInside,
// serving the operator's Files page and editor. Then agents gained tools that
// read and write the same directory, and gears gained an in/ and an out/ — and
// a second implementation of "does this path escape the workspace" is not a
// second safeguard. It is the one that will be subtly weaker, and it will not
// be the one anybody reads.
//
// Nothing here touches a database, an HTTP request or a model, so the engine
// and the gear executor can hold it without dragging the server in behind it.
package workdir

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// The layout inside a workspace directory. These are named here rather than
// spelled as literals at each site because three packages now write into the
// same tree — the HTTP layer lands inlet deliveries, the gear executor lands
// what a gear produced, and the engine reads both — and a directory name that
// drifts between them is a file nobody can find.
const (
	// InletDir holds files delivered through an inlet: bytes a caller outside
	// this workspace chose. What is under here is third-party text by
	// construction, and the engine treats it as such.
	InletDir = "inlets"
	// GearOutDir holds what gears left in their out/ directories, one
	// subdirectory per run.
	GearOutDir = "gears"
	// AttachmentDir holds files the operator attached to a message. It is
	// separate from InletDir for one reason and it is not tidiness: what is
	// under InletDir was written by somebody outside this workspace and is
	// fenced as untrusted wherever an agent reads it, and an operator's own
	// file is not. Landing both in one directory would mean either fencing the
	// operator's words or unfencing a caller's.
	AttachmentDir = "attachments"
)

// Dir is the per-workspace directory: the operator's Files page and editor,
// the workspace terminal, the files an inlet delivers, and what agents and
// gears read and write. It is created on demand rather than at workspace
// creation, so an install that predates it needs no migration.
//
// An empty return means the directory could not be made; ResolveInside turns
// that into a refusal rather than letting a caller operate on the process's
// working directory by accident.
func Dir(dataDir string, wsID int64) string {
	dir := filepath.Join(dataDir, "workspaces", strconv.FormatInt(wsID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Error("could not create workspace directory", "workspace_id", wsID, "err", err)
		return ""
	}
	return dir
}

// Remove deletes a workspace's directory and everything under it.
//
// It is separate from Dir because Dir CREATES — a caller that merely wants to
// name the directory would otherwise bring it back into existence on the way to
// deleting it.
//
// It exists because deleting a workspace did not delete anything on disk. The
// row went, the agents and the timeline cascaded, and the whole tree stayed:
// every file an inlet had delivered, everything gears had produced, every
// attachment the operator had sent. On a single ReadWriteOnce volume that is not
// untidiness, it is a disk that fills with the contents of workspaces nobody can
// reach any more.
//
// A missing directory is success: a workspace that never had files has nothing
// to reclaim, and reporting that as an error would make the caller decide
// whether "it was not there" is a problem. It is not.
func Remove(dataDir string, wsID int64) error {
	dir := filepath.Join(dataDir, "workspaces", strconv.FormatInt(wsID, 10))
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove the workspace directory %s: %w", dir, err)
	}
	return nil
}

// Clean is the one answer to "what does this caller-supplied path mean,
// relative to the workspace root": slash-separated, no leading slash, every
// ".." already collapsed. The workspace root itself is "".
//
// Callers that need to name the same file back to whoever asked for it — a
// file tree's entries, a gear's in/ — use this rather than the resolved
// absolute path, so what they report is the path the caller can pass in again.
func Clean(rel string) string {
	return strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/")
}

// ResolveInside turns a caller-supplied relative path into an absolute one and
// proves it stays inside root. Symlinks are resolved first: a link pointing out
// of the workspace would otherwise pass a textual prefix check and hand out the
// server's own files.
func ResolveInside(root, rel string) (string, error) {
	if root == "" {
		return "", errors.New("this workspace has no working directory")
	}
	full := filepath.Join(root, filepath.FromSlash(Clean(rel)))

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		// Nothing at that path yet — saving "docs/notes.md" into an empty
		// workspace must work, so containment is proved against the deepest
		// ancestor that does exist. The remainder cannot escape: Clean has
		// already collapsed every "..".
		anc := filepath.Dir(full)
		for {
			realAnc, err := filepath.EvalSymlinks(anc)
			if err == nil {
				if !within(realRoot, realAnc) {
					return "", errors.New("path leaves the workspace")
				}
				return full, nil
			}
			if !os.IsNotExist(err) {
				return "", err
			}
			parent := filepath.Dir(anc)
			if parent == anc {
				return "", errors.New("path leaves the workspace")
			}
			anc = parent
		}
	}
	if !within(realRoot, real) {
		return "", errors.New("path leaves the workspace")
	}
	return real, nil
}

func within(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}
