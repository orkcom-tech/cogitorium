// Package mcpcatalog is the list of MCP servers an operator can install by
// choosing one, rather than by knowing that Jira's server is an npm package,
// what its binary is called, which arguments it takes and which environment
// variables it reads.
//
// WHERE THE LIST COMES FROM, which was the decision worth making carefully.
// Three sources were possible: shipped in the binary, fetched from a repository
// we publish, or written by the operator. This is the first, and the third is
// the mcp_servers table that already exists — an operator has always been able
// to install anything by naming its command, and the interesting MCP server is
// very often internal.
//
// The second is deliberately NOT here. A catalogue fetched at runtime is a list
// that can change under an install between the day it was reviewed and the day
// somebody installs from it, and this product does not make outbound requests
// nobody agreed to. If it is ever added it belongs behind the same switch as
// the update check, because an install that does not phone home should not
// acquire a catalogue that does.
//
// WHAT AN ENTRY IS NOT. It is not an installer and not a one-click anything. It
// fills in the fields of the form an operator would otherwise have typed, and
// they still install, probe, read what it claims to offer, approve the server,
// approve each tool, and grant it. Every one of those is unchanged: an entry
// removes the need to look up an npm package name, not the need to decide.
package mcpcatalog

import "strings"

// Entry is one known server.
type Entry struct {
	// ID is stable and referenced by the interface; never reuse one.
	ID string `json:"id"`
	// Name is what the server will be called if installed as offered. It has to
	// satisfy mcpstore's name rule: lowercase, digits, dash and underscore.
	Name string `json:"name"`
	// Title and Reaches are for a person: what this is, and what it touches,
	// in a sentence somebody recognises rather than a package description.
	Title   string `json:"title"`
	Reaches string `json:"reaches"`

	Command string   `json:"command"`
	Args    []string `json:"args"`

	// EnvNames are the credentials this server needs, BY NAME. The value never
	// appears here, is never sent to a model, and is resolved at spawn through
	// the same secrets resolver a gear's names go through. An operator sets
	// them under Variables before the server will work.
	EnvNames []string `json:"env_names"`

	// Needs is the install prerequisite, stated rather than discovered. Most
	// MCP servers are an `npx` or a `uvx` away, which means node or python on
	// the machine — and an entry that does not say so produces a spawn failure
	// whose message nobody can read.
	Needs string `json:"needs"`

	// Docs is where to go to find out what the arguments mean and how to get
	// the credential. Not fetched by this product; shown as a link.
	Docs string `json:"docs"`
}

// Fetched at spawn is the fact that decides how an entry should be read, and it
// is true of every one of these: `npx pkg@latest` downloads from a registry
// every time it starts. The thing approved on Tuesday is not necessarily the
// thing that runs on Friday, and no fingerprint over a command line can catch
// it. That sentence belongs on the review screen, not in a footnote, so it
// lives here where the interface can render it verbatim.
const FetchedAtSpawn = "This server's code is downloaded when it starts, every time it starts. " +
	"Approving it approves the command line, not the bytes that command will fetch tomorrow."

// The list.
//
// SHORT ON PURPOSE. Every entry is a claim this project makes about somebody
// else's software — that this is the right package, that these are the
// arguments, that this is the credential it wants — and a claim that is wrong
// sends an operator to debug a spawn failure in a product that told them it
// would work. So this holds servers whose command line is stable and
// documented, and an operator who wants anything else installs it by name,
// which has always worked and is one screen away.
var entries = []Entry{
	{
		ID:      "filesystem",
		Name:    "filesystem",
		Title:   "Files in a directory",
		Reaches: "whichever directory you name, on this machine, with this server's own file access",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/path/to/expose"},
		Needs:   "node (npx). Edit the last argument to the directory you mean before approving it.",
		Docs:    "https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem",
	},
	{
		ID:      "git",
		Name:    "git",
		Title:   "A git repository",
		Reaches: "the history, branches and diffs of one repository on this machine",
		Command: "uvx",
		Args:    []string{"mcp-server-git", "--repository", "/path/to/repo"},
		Needs:   "python (uvx). Edit the repository path before approving it.",
		Docs:    "https://github.com/modelcontextprotocol/servers/tree/main/src/git",
	},
	{
		ID:       "github",
		Name:     "github",
		Title:    "GitHub issues and pull requests",
		Reaches:  "every repository the token can see — which is usually more than the one you had in mind",
		Command:  "npx",
		Args:     []string{"-y", "@modelcontextprotocol/server-github"},
		EnvNames: []string{"GITHUB_PERSONAL_ACCESS_TOKEN"},
		Needs:    "node (npx), and a personal access token set under Variables as GITHUB_PERSONAL_ACCESS_TOKEN.",
		Docs:     "https://github.com/modelcontextprotocol/servers/tree/main/src/github",
	},
	{
		ID:       "postgres",
		Name:     "postgres",
		Title:    "A PostgreSQL database, read-only",
		Reaches:  "the schema and rows the connection string's role can read",
		Command:  "npx",
		Args:     []string{"-y", "@modelcontextprotocol/server-postgres"},
		EnvNames: []string{"POSTGRES_CONNECTION_STRING"},
		Needs: "node (npx), and a connection string under Variables as POSTGRES_CONNECTION_STRING. " +
			"Point it at a read-only role: this grants an agent whatever that role has.",
		Docs: "https://github.com/modelcontextprotocol/servers/tree/main/src/postgres",
	},
	{
		ID:       "slack",
		Name:     "slack",
		Title:    "Slack channels and messages",
		Reaches:  "the channels the bot token is in, including their history",
		Command:  "npx",
		Args:     []string{"-y", "@modelcontextprotocol/server-slack"},
		EnvNames: []string{"SLACK_BOT_TOKEN", "SLACK_TEAM_ID"},
		Needs:    "node (npx), and SLACK_BOT_TOKEN plus SLACK_TEAM_ID under Variables.",
		Docs:     "https://github.com/modelcontextprotocol/servers/tree/main/src/slack",
	},
	{
		ID:       "sentry",
		Name:     "sentry",
		Title:    "Sentry issues",
		Reaches:  "the issues and stack traces of the projects the token covers",
		Command:  "uvx",
		Args:     []string{"mcp-server-sentry", "--auth-token", "$SENTRY_TOKEN"},
		EnvNames: []string{"SENTRY_TOKEN"},
		Needs:    "python (uvx), and SENTRY_TOKEN under Variables.",
		Docs:     "https://github.com/modelcontextprotocol/servers/tree/main/src/sentry",
	},
}

// All returns the catalogue. A copy, because the caller marshals it and a
// package-level slice handed out by reference is a slice somebody eventually
// sorts in place.
func All() []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// Get returns one entry by id.
func Get(id string) (Entry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Search filters by a word an operator typed. Matching on the title and what it
// reaches as well as the name, because somebody looking for their issue tracker
// types "issues" or "tickets" rather than "sentry".
func Search(q string) []Entry {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return All()
	}
	var out []Entry
	for _, e := range entries {
		hay := strings.ToLower(e.ID + " " + e.Name + " " + e.Title + " " + e.Reaches)
		if strings.Contains(hay, q) {
			out = append(out, e)
		}
	}
	return out
}
