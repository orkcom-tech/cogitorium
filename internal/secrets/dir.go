package secrets

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// dirSource is a directory where each file's NAME is a name and its CONTENTS
// are the value.
//
// This is the shape Kubernetes already speaks: the chart mounts a ConfigMap
// and a Secret as directories, and the server reads files. Reading files also
// gets rotation for free — kubelet rewrites the contents in place, and the next
// gear run picks up the new value with nothing restarted.
//
// It is deliberately NOT a Kubernetes API client. Talking to the API needs a
// service account token in the pod, and the chart mounts none on purpose:
// agent-authored code runs in that pod, and a cluster credential is the one
// thing it must not find.
type dirSource struct {
	path string
	kind string
	// label is what an operator is told won, e.g. "the secrets directory".
	label string
}

// Check proves the directory is readable at startup rather than at the first
// gear run. An operator who configured a directory the server cannot open has
// made a mistake they can fix in one minute now, or debug for an hour later.
func (d dirSource) Check() error {
	if d.path == "" {
		return nil
	}
	info, err := os.Stat(d.path)
	if err != nil {
		return fmt.Errorf("%s %s cannot be read: %w — create it, mount it, or remove the setting", d.label, d.path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %s is a file, not a directory: this source is one file per name, so point it at the directory holding them", d.label, d.path)
	}
	return nil
}

// read collects everything the directory currently offers.
//
// Entries whose name begins with "." are skipped, and that is load-bearing
// rather than tidiness: Kubernetes' atomic writer keeps the real data in
// "..data" and timestamped sibling directories, and links the visible names
// into it. Taking those would produce names no gear ever declares and, worse,
// would make a rotation visible as a duplicate set.
func (d dirSource) read() map[string]Value {
	out := map[string]Value{}
	if d.path == "" {
		return out
	}
	entries, err := os.ReadDir(d.path)
	if err != nil {
		// Not fatal here: startup already checked, so this is the directory
		// disappearing under a running server. The gear run that needed a name
		// from it will refuse by name, which is the clearer message.
		slog.Warn("could not read a named-value directory", "dir", d.path, "kind", d.kind, "err", err)
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(d.path, name)
		// Stat, not e.IsDir: the visible entries in a Kubernetes mount are
		// symlinks, and DirEntry reports the link rather than what it points at.
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		if err := ValidName(name); err != nil {
			// Said out loud, because the alternative is an operator staring at
			// a file they mounted on purpose and a gear that says the name does
			// not resolve, with nothing connecting the two.
			slog.Warn("a file in a named-value directory is not a usable name and was ignored",
				"dir", d.path, "file", name, "reason", err)
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			slog.Warn("could not read a named value from its file", "dir", d.path, "file", name, "err", err)
			continue
		}
		value := trimFileNewline(string(raw))
		if value == "" {
			// An empty file is a missing value wearing a name. Skipping it here
			// makes the gear's refusal name the name, which is the failure the
			// operator can act on; taking it would inject "" and fail inside
			// somebody's API client.
			slog.Warn("a named value's file is empty and was ignored", "dir", d.path, "file", name)
			continue
		}
		out[name] = Value{Name: name, Value: value, Kind: d.kind, Source: d.label + " (" + d.path + ")"}
	}
	return out
}

// trimFileNewline removes the line terminator a shell adds, and nothing else.
//
// `echo hunter2 > api-key` writes a trailing newline that is not part of the
// credential, and a key with a stray "\n" fails at the far end of an HTTP
// request with a message about nothing. Kubernetes writes exact bytes with no
// terminator, so trimming one never damages a mounted value.
//
// A multi-line file — a PEM, a JSON blob — is taken byte for byte: there the
// final newline may well be part of the value, and only a single-line file's
// trailing newline is unambiguously the line ending rather than data.
func trimFileNewline(s string) string {
	body, ok := strings.CutSuffix(s, "\n")
	if !ok {
		return s
	}
	body = strings.TrimSuffix(body, "\r")
	if strings.ContainsAny(body, "\n\r") {
		return s
	}
	return body
}
