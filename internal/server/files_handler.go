package server

import (
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// The file tree exists so a quick look or a small correction does not mean
// leaving for an IDE while agents are working. It is scoped exactly like the
// workspace terminal — same directory, same members — because it is the same
// blast radius, and a second, weaker gate on the same files would be a hole
// dressed up as a feature.

// maxEditableBytes bounds what the editor will open. A file larger than this
// is not refused because editing it is wrong, but because shipping it through
// a browser textarea is: the operator gets told the size instead of a hung tab.
const maxEditableBytes = 2 << 20 // 2 MiB

type fileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // relative to the workspace root, always slash-separated
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// The path validation these handlers run on is workdir.ResolveInside. It used
// to live here, and moved out when agents and gears gained access to the same
// directory: three packages proving containment three times is three chances
// to prove it slightly differently.

// handleListFiles lists one directory rather than walking the whole tree: a
// deep node_modules would otherwise turn opening the panel into a stall, and
// the UI expands a level at a time anyway.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	root := s.workspaceDir(id)
	rel := r.URL.Query().Get("path")
	dir, err := workdir.ResolveInside(root, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "no such directory in this workspace")
			return
		}
		fail(w, r, err)
		return
	}

	base := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	out := make([]fileEntry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue // vanished mid-listing; not worth failing the whole call
		}
		e := fileEntry{
			Name:  it.Name(),
			Path:  strings.TrimPrefix(path.Join(base, it.Name()), "/"),
			Dir:   it.IsDir(),
			Mtime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		}
		if !it.IsDir() {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	// Directories first, then names: the order a file tree is read in.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, http.StatusOK, out)
}

// handleReadFile returns a file's text. Binary content is refused rather than
// mangled: a textarea round-trip would silently corrupt it on save.
func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	full, err := workdir.ResolveInside(s.workspaceDir(id), rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "no such file in this workspace")
			return
		}
		fail(w, r, err)
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "that is a directory")
		return
	}
	if info.Size() > maxEditableBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			"this file is too large to open in the editor — use the terminal for it")
		return
	}

	b, err := os.ReadFile(full)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !utf8.Valid(b) {
		writeError(w, http.StatusUnsupportedMediaType,
			"this looks like a binary file; the editor would corrupt it on save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    rel,
		"content": string(b),
		"mtime":   info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// handleWriteFile saves an edit, creating the file if it is new. Directories
// on the way are created too, so saving "docs/notes.md" in an empty workspace
// works rather than erroring about a missing parent.
func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	full, err := workdir.ResolveInside(s.workspaceDir(id), rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxEditableBytes+1))
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(body) > maxEditableBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "that content is too large to save from the editor")
		return
	}
	if info, err := os.Stat(full); err == nil && info.IsDir() {
		writeError(w, http.StatusBadRequest, "that is a directory")
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		fail(w, r, err)
		return
	}
	if err := os.WriteFile(full, body, 0o600); err != nil {
		fail(w, r, err)
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  rel,
		"size":  info.Size(),
		"mtime": info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
	})
}
