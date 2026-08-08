package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
)

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	var e apiError
	e.Error.Message = msg
	writeJSON(w, code, e)
}

// fail maps domain errors to HTTP codes and logs them — every error path is
// visible in the log, none are swallowed.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, catalog.ErrConflict):
		code = http.StatusConflict
	}
	slog.Error("api error", "method", r.Method, "path", r.URL.Path, "status", code, "err", err)
	writeError(w, code, err.Error())
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id in path")
		return 0, false
	}
	return id, true
}

func parseID(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// isDomainError reports whether err is one of the shared sentinel errors,
// which the fail() mapping already turns into the right status.
func isDomainError(err, sentinel error) bool { return errors.Is(err, sentinel) }
