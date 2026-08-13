package server

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// Reading back through the door a delivery came in by.
//
// An inlet key used to be write-only: it opened one route and could read
// nothing at all. So the only way to let a pipeline poll its own job was to
// hand it a user token — a credential that can also delete workspaces and
// approve agent-authored code. That is a wildly disproportionate answer to
// "may I see the job I just sent", and it is the reason asynchronous delivery
// could not exist: 202 with a run number is useless if nothing can then fetch
// the run.
//
// Deliberately NOT a scope column on tokens, and not a policy system. The
// requirement is one sentence — an inlet key may read the runs that arrived
// through its own inlet — and two WHERE terms express it. A general mechanism
// built to serve one sentence is a mechanism every future route then has to be
// audited against.

// inletFromKey resolves the door a request presents a key for, or writes the
// refusal itself. One proof of an inlet credential, used by every route under
// /i/, so there is a single place where "this key opens this door" is decided.
func (s *Server) inletFromKey(w http.ResponseWriter, r *http.Request) (inlet.Inlet, bool) {
	address := r.PathValue("address")
	in, err := s.inlets.ByAddress(r.Context(), address)
	if err != nil {
		if errors.Is(err, inlet.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such inlet")
			return inlet.Inlet{}, false
		}
		fail(w, r, err)
		return inlet.Inlet{}, false
	}
	if !in.MatchesKey(bearerToken(r)) {
		slog.Warn("inlet request refused: key does not open this door",
			"address", address, "path", r.URL.Path, "remote", r.RemoteAddr, "key_issued", in.HasKey)
		writeError(w, http.StatusUnauthorized,
			"this inlet's key is required: send Authorization: Bearer <inlet key>. "+
				"A key is issued from the workspace's inlet settings and shown once")
		return inlet.Inlet{}, false
	}
	s.inlets.NoteKeyUse(r.Context(), in.ID)
	return in, true
}

// runForKey resolves a run a caller may see: it must have arrived through the
// inlet whose key was presented.
//
// The check is on the RUN's inlet rather than on its workspace. Two doors into
// one workspace are two different callers, and a key for one has no business
// reading the other's payload sizes, error text and record.
func (s *Server) runForKey(w http.ResponseWriter, r *http.Request) (inlet.Run, bool) {
	in, ok := s.inletFromKey(w, r)
	if !ok {
		return inlet.Run{}, false
	}
	id, ok := pathID(w, r)
	if !ok {
		return inlet.Run{}, false
	}
	run, err := s.inlets.GetRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, inlet.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such run")
			return inlet.Run{}, false
		}
		fail(w, r, err)
		return inlet.Run{}, false
	}
	if run.InletID == nil || *run.InletID != in.ID {
		// 404 rather than 403: a caller holding one door's key should not be
		// able to learn that a run id exists behind another.
		writeError(w, http.StatusNotFound, "no such run")
		return inlet.Run{}, false
	}
	return run, true
}

// handleInletRunStatus is what a caller polls after an asynchronous delivery.
func (s *Server) handleInletRunStatus(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runForKey(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, deliveryResponse{
		Run: run.ID, State: run.State, Result: run.Result,
		Error: run.Error, Did: recordFrom(run.Did),
	})
}

// wantsAsync reports whether the caller asked not to be kept waiting.
//
// The header is RFC 7240's `Prefer: respond-async`, which exists for exactly
// this and means a caller does not have to learn a Cogitorium-specific
// convention. Synchronous stays the default: every caller written against this
// door so far expects the answer in the response, and a queue that silently
// changed that would break all of them at once.
func wantsAsync(r *http.Request) bool {
	for _, v := range r.Header.Values("Prefer") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "respond-async") {
				return true
			}
		}
	}
	return false
}

// handleInletRunFile serves a file this run produced, or the payload it was
// given.
//
// The Record has always named files with paths and sizes, and its own comment
// claimed "the path in the record is the path a caller can fetch". That was
// false: the only file route was under a user token, capped at 2 MiB, refused
// anything that was not UTF-8, and returned content as a JSON string — so bytes
// could not have survived it even with the guards lifted. A caller was told a
// file existed and given no way to get it.
//
// AUTHORISATION IS THE RUN'S OWN RECORD. The requested path must be the payload
// this run was given, or one of the files the engine recorded it producing.
// That is narrower than "any path inside the workspace" and narrower on
// purpose: a delivery's key would otherwise be a way to read every file any
// other job in that workspace had left, which is not what handing somebody a
// door key means.
func (s *Server) handleInletRunFile(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runForKey(w, r)
	if !ok {
		return
	}
	rel := workdir.Clean(r.URL.Query().Get("path"))
	if rel == "" {
		writeError(w, http.StatusBadRequest,
			"name the file with ?path=, using the workspace-relative path the run's record gives")
		return
	}
	if !runProduced(run, rel) {
		// 404 rather than 403: a caller must not be able to map the workspace
		// by watching which paths change the answer.
		writeError(w, http.StatusNotFound,
			"this run's record does not name that file, so it is not this run's to fetch")
		return
	}

	root := workdir.Dir(s.dataDir, run.WorkspaceID)
	if root == "" {
		fail(w, r, errors.New("this workspace has no directory on this machine"))
		return
	}
	full, err := workdir.ResolveInside(root, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusGone,
				"this file is named in the run's record but is no longer on disk")
			return
		}
		fail(w, r, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "not a file")
		return
	}

	// Streamed, with no size ceiling and no encoding opinion: what a gear
	// produced is whatever it produced, and a route that could only carry small
	// text would be a route the interesting jobs cannot use.
	w.Header().Set("Content-Type", contentTypeFor(rel))
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(rel)+`"`)
	http.ServeContent(w, r, filepath.Base(rel), info.ModTime(), f)
}

// runProduced reports whether a path is one this run is entitled to hand back:
// the payload it was given, or a file its record says it made.
func runProduced(run inlet.Run, rel string) bool {
	if run.PayloadPath != "" && workdir.Clean(run.PayloadPath) == rel {
		return true
	}
	for _, f := range recordFrom(run.Did).Files {
		if workdir.Clean(f.Path) == rel {
			return true
		}
	}
	return false
}

// contentTypeFor is a guess from the extension, and deliberately falls back to
// octet-stream rather than sniffing: a caller fetching a file by name knows
// what it asked for, and sniffing is how a route ends up serving somebody
// else's HTML as HTML.
func contentTypeFor(rel string) string {
	if ct := mime.TypeByExtension(filepath.Ext(rel)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
