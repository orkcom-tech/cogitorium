package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/bundle"
)

// maxBundleBytes bounds an imported document. A bundle carries gear source
// and context documents, so it is legitimately larger than any other request
// body here — but import is reachable by every signed-in user, and an
// unbounded decode is a way to exhaust the server's memory from one request.
const maxBundleBytes = 8 << 20

// bundleStores hands the format its four stores as one value, so a caller
// cannot pass the gear store where the catalog belongs.
func (s *Server) bundleStores() bundle.Stores {
	return bundle.Stores{
		Workspaces: s.workspaces,
		Catalog:    s.catalog,
		Gears:      s.gears,
		Context:    s.context,
	}
}

// handleExportWorkspace writes a workspace out as a portable document. It is
// gated exactly like reading the workspace, because that is what it is: a
// caller who can already see every agent, wire and gear here learns nothing
// new from having them in one file.
func (s *Server) handleExportWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	gears, err := queryFlag(r, "gears")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	withContext, err := queryFlag(r, "context")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	b, err := bundle.Export(r.Context(), s.bundleStores(), id, bundle.Options{Gears: gears, Context: withContext})
	if err != nil {
		failContext(w, r, err)
		return
	}
	// The filename is built from the workspace name, which is operator text:
	// bundle.Filename is what keeps it from closing the quoted header value
	// or naming a directory.
	w.Header().Set("Content-Disposition", `attachment; filename="`+bundle.Filename(b.Workspace.Name)+`"`)
	writeJSON(w, http.StatusOK, b)
}

// handleImportWorkspace builds a new workspace from a bundle, owned by the
// caller. It is not workspace-scoped — there is no workspace yet — and needs
// no more privilege than creating one by hand, which is what it amounts to.
func (s *Server) handleImportWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name           string        `json:"name"`
		Bundle         bundle.Bundle `json:"bundle"`
		IncludeGears   bool          `json:"include_gears"`
		IncludeContext bool          `json:"include_context"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBundleBytes)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("this bundle is larger than the %d MB import limit", maxBundleBytes>>20))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	caller := callerFrom(r.Context())
	res, err := bundle.Import(r.Context(), s.bundleStores(), in.Bundle, bundle.ImportOptions{
		Name:           in.Name,
		OwnerID:        caller.ID,
		IncludeGears:   in.IncludeGears,
		IncludeContext: in.IncludeContext,
	})
	if err != nil {
		// A malformed document is the caller's to fix, so it must not read as
		// a server fault; everything else keeps the mapping the rest of the
		// API uses.
		if errors.Is(err, bundle.ErrMalformed) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// queryFlag reads an on/off query parameter. An unrecognised value is refused
// rather than read as "off": an operator who asked for gears=yes and got a
// bundle without gears would have no way to tell it was their typo.
func queryFlag(r *http.Request, name string) (bool, error) {
	switch v := r.URL.Query().Get(name); v {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 1 or 0 (got %q)", name, v)
	}
}
