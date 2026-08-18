// Package mcpcatalog is the library an operator picks a server from, rather
// than knowing that their issue tracker's server is an npm package, what its
// binary is called, which arguments it takes and which environment variables it
// reads.
//
// # Where the list comes from
//
// The official registry at registry.modelcontextprotocol.io, read live — see
// registry.go for the shape and for why this is fetched rather than compiled
// in. The short version: it holds thousands of servers, two thirds of them
// hosted services rather than packages, and a hand-written list is not a
// smaller version of that but a different and much worse thing.
//
// # What the fetch is gated on
//
// The same switch the update check uses. An install that has not agreed to make
// outbound requests has no library and is told so plainly; `add by hand` is
// unaffected, because it never left the machine to begin with. That is the
// whole answer to "an install that does not phone home should not acquire a
// catalogue that does" — it does not acquire one.
//
// # What an entry is not
//
// It is not an installer and not a one-click anything. It fills in the fields
// of the form an operator would otherwise have typed, and they still install,
// probe, read what the server claims to offer, approve the server, approve each
// tool, and grant it. Nothing here is executed and nothing here is trusted: a
// registry entry is somebody else's description of somebody else's software.
package mcpcatalog

// Entry is one server the library offers.
//
// It carries the stdio half or the remote half, never both. Which one it got
// was decided when the registry answer was read, and a remote is preferred
// where a server publishes both — a hosted endpoint runs no code on this
// machine, and a package is a download that executes here as this server's
// user, outside the sandbox.
type Entry struct {
	// ID is the registry's own name for the server, which is globally unique
	// and stable. Name is what it will be called HERE, squeezed into the
	// lower-case shape mcpstore accepts.
	ID   string `json:"id"`
	Name string `json:"name"`

	// Title and Reaches are for a person: what this is, and what it touches.
	Title   string `json:"title"`
	Reaches string `json:"reaches"`

	// Transport is "stdio", "streamable-http" or "sse".
	Transport string `json:"transport"`

	// The stdio half: a package to run on this machine.
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// EnvNames are the credentials it needs, BY NAME. The value never appears
	// here, is never sent to a model, and is resolved at spawn through the same
	// resolver a gear's names go through.
	EnvNames []string `json:"env_names"`

	// The remote half: a hosted service to call. HeaderNames maps a header to
	// the NAMED value an operator will have to set — never a value.
	URL         string            `json:"url"`
	HeaderNames map[string]string `json:"header_names"`

	// Needs is the prerequisite, stated rather than discovered. Most packaged
	// servers are an `npx` or a `uvx` away, which means node or python on the
	// machine, and an entry that did not say so would produce a spawn failure
	// whose message nobody can read.
	Needs string `json:"needs"`

	// Docs is where to find out what the arguments mean and how to get the
	// credential. Not fetched by this product; shown as a link.
	Docs string `json:"docs"`
}

// Installable reports whether this entry produced something this install can
// actually connect to. An entry that did not is never returned at all — see
// entryFrom — so this is the invariant rather than a filter.
func (e Entry) Installable() bool {
	switch e.Transport {
	case "stdio":
		return e.Command != ""
	case "streamable-http", "sse":
		return e.URL != ""
	default:
		return false
	}
}

// FetchedAtSpawn is the fact that decides how an entry should be read, and it
// differs by shape. A packaged server's code is downloaded every time it
// starts, so what an operator approves is the command line and not the bytes
// that command will fetch tomorrow — no fingerprint can catch that. A hosted
// server runs nothing here at all; what leaves instead is a credential and
// whatever the agents say.
//
// It lives here so the interface renders the same sentence the review screen
// does rather than inventing a softer one.
const FetchedAtSpawn = "A packaged server's code is downloaded when it starts, every time it starts. " +
	"Approving it approves the command line, not the bytes that command will fetch tomorrow. " +
	"A hosted server runs no code here at all — what leaves instead is your credential and whatever your agents say."
