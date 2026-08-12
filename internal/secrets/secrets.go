// Package secrets holds the named values a gear is given at run time.
//
// A gear is given NAMES; it reads the VALUES from its environment. The model
// never sees a value, and that is the whole point: an agent's answer leaves
// the building — in an inlet response, in the chat — so a secret in a prompt
// is a secret published. Names are safe to show, to log, to export and to put
// in front of the operator at approval; values are not.
//
// variables and secrets are one mechanism with one difference. A variable's
// value is shown in the interface and in the record. A secret's is shown once
// when it is set and never again, and is redacted everywhere it could
// otherwise surface — logs, a gear's recorded stdout and stderr, the run
// record, an export bundle. See redact.go for the enforcement.
package secrets

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	KindVariable = "variable"
	KindSecret   = "secret"
)

// ErrUnresolved marks a name nothing could supply a value for. It is its own
// error because the caller has to be able to say WHICH gear asked, and the
// resolver does not know.
var ErrUnresolved = errors.New("no value for this name")

// nameRe is the shape of an environment variable name, and deliberately not
// one character wider.
//
// Uppercase is required rather than applied: the same name has to match a file
// in a mounted directory byte for byte, and silently upper-casing "api_key"
// here would leave an operator staring at a file called api_key that nothing
// ever reads.
var nameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// reserved names belong to the sandbox itself. A gear that replaced PATH could
// not find its own interpreter, and one that replaced COGITORIUM_GEAR would lie
// about which gear it is — so these are refused where they are typed rather
// than quietly losing to the fixed environment at run time.
//
// The proxy names are here for a sharper reason than tidiness. A gear granted
// the network is given them so its traffic is checked against the destinations
// the operator allowed and written into the connection log; a name that
// overrode one would be an attempt to route around that. It would fail — the
// fixed environment is written last and wins — but failing silently is how an
// operator ends up with a variable they set and nothing reading it.
var reserved = map[string]bool{
	"PATH": true, "HOME": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
}

// ValidName is the one place a name is judged, so what an operator may set,
// what a gear may declare and what a bundle may carry cannot drift apart.
func ValidName(name string) error {
	if name == "" {
		return errors.New("a name is required")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a usable name: use A-Z, digits and underscores, starting with a letter (up to 64 characters), e.g. API_KEY", name)
	}
	if reserved[name] || strings.HasPrefix(name, "COGITORIUM_") {
		return fmt.Errorf("%q is the sandbox's own environment and cannot be redefined: pick another name, e.g. GEAR_%s", name, name)
	}
	return nil
}

// ValidKind rejects anything the store's CHECK constraint would reject later,
// where the error would name a column instead of the choice that was made.
func ValidKind(kind string) error {
	if kind != KindVariable && kind != KindSecret {
		return fmt.Errorf("kind must be %q or %q (got %q): a variable's value is shown afterwards, a secret's is not",
			KindVariable, KindSecret, kind)
	}
	return nil
}

// NormalizeNames trims, validates, removes duplicates and sorts a declared
// list.
//
// Sorted because the list is read by a person at approval and compared against
// the previous version's; an order that depends on how the forging agent
// happened to type them makes two identical declarations look different.
func NormalizeNames(names []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if err := ValidName(n); err != nil {
			return nil, err
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Value is one resolved name, ready to be put in a gear's environment.
type Value struct {
	Name string
	// Value is the plaintext. It exists in memory for the length of one run
	// and must not be written anywhere — see Record for what may be shown.
	Value string
	Kind  string
	// Source says which of the three sources won, in the operator's words. It
	// is the answer to "why is this gear seeing staging's key", and it never
	// carries the value.
	Source string
}

func (v Value) IsSecret() bool { return v.Kind == KindSecret }

// Record is what a name looks like to anyone reading the interface: a secret's
// Value is empty here, always, because this type is what gets marshalled to
// JSON and sent to a browser.
type Record struct {
	ID int64 `json:"id"`
	// WorkspaceID nil means the install-wide value.
	WorkspaceID *int64 `json:"workspace_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	// Value carries a variable's value and is empty for a secret. Not
	// "omitempty": a caller reading this field must be able to tell "this is a
	// secret, so there is nothing here" from "this key was not in the response".
	Value       string `json:"value"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// Status describes one declared name without disclosing anything: whether this
// install can supply it, what kind it would be, and which source would win.
// It is what the approval screen reads — an operator approving a gear that
// names something nothing supplies is approving a gear that cannot run.
type Status struct {
	Name string `json:"name"`
	// Found false means a run of this gear would be refused, naming this name.
	Found  bool   `json:"found"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}
