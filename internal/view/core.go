package view

import (
	"embed"
	"html/template"
	"io/fs"
	"strings"
)

// The host's own layer, and the model registry beside it.
//
// The pair (template name, model) is the API a plugin author writes against.
// The markup underneath is not, and is never promised — which is the whole
// reason an override can survive a redesign.

//go:embed templates/*.html
var coreTemplates embed.FS

// The hypermedia layer, vendored.
//
// Served from this binary rather than a CDN because the interface fetches
// nothing from the network — no fonts, no scripts, no analytics — and an
// exception for a library would read the same in a packet capture as any other
// exception.
//
//go:embed assets/htmx.min.js assets/htmx-sse.js
var hypermedia embed.FS

// Hypermedia is htmx and its SSE extension, for the routes that serve them.
func Hypermedia() fs.FS {
	sub, err := fs.Sub(hypermedia, "assets")
	if err != nil {
		panic("view: the vendored hypermedia layer is unreadable: " + err.Error())
	}
	return sub
}

// Core is the host's template layer. It is layer zero of every composition.
func Core() fs.FS {
	sub, err := fs.Sub(coreTemplates, "templates")
	if err != nil {
		// The embed is compiled in, so this cannot fail at runtime. If it
		// somehow does, there is no shell to serve and pretending otherwise
		// would produce a blank page instead of a stack trace.
		panic("view: the embedded core templates are unreadable: " + err.Error())
	}
	return sub
}

// Ctx rides on every model.
//
// It exists so a deeply nested override never has to ask the host to thread
// context down five levels before it can render. An author who overrides one
// row and needs to know who is looking at it should not have to file an issue.
type Ctx struct {
	// Viewer is who is signed in. The zero value is nobody.
	Viewer Viewer
	// Workspace is the one in scope, zero when none is.
	Workspace Workspace
	// InstallMode is how this install is reached — "local" or "remote" — which
	// is the difference an author needs when deciding whether to show
	// something that only makes sense on somebody's own machine.
	InstallMode string
	// Path is the request path, so an override can mark its own nav entry
	// current without the host having to pass that in separately.
	Path string
	// Theme is the operator's choice, empty when they follow the system.
	Theme string
	// Lang is the document language.
	Lang string
	// RequestToken is what a form has to carry back. Present on every model so
	// a plugin's form is never the one that forgot it.
	RequestToken string
	// T holds the interface strings the shell itself needs. A plugin rendering
	// host chrome should not have to hard-code English into a template that
	// will be read in another language.
	T Strings
}

// Viewer is the signed-in user, reduced to what a template legitimately needs.
// Deliberately not the identity record: a template has no business holding a
// password hash or a token, and a model that carried one would eventually
// render it.
type Viewer struct {
	ID      int64
	Name    string
	IsAdmin bool
	// SignedIn is false for the zero value, so a template can ask directly
	// rather than comparing an ID against zero and getting it subtly wrong.
	SignedIn bool
}

// Workspace is the one in scope.
type Workspace struct {
	ID   int64
	Name string
}

// Strings are the few words the host's own chrome renders.
type Strings struct {
	Navigation string
	// Instructions is the library's own title. Here rather than written into
	// the template because a plugin translating the product should not have to
	// take over a page to change one word.
	Instructions string
	// Models is the model catalog's title, for the same reason.
	Models string
	// Context is the context space's title.
	Context string
	// Gears is the gear catalogue's title.
	Gears string
	// Workspaces is the landing screen's title.
	Workspaces string
	// Variables is the install-wide named values' title.
	Variables string
	// Plugins is the library screen's title.
	Plugins string
}

// DefaultStrings is English, which is what this product ships in today. It is
// a value rather than constants so a plugin overriding the shell can be handed
// a different one without the host changing shape first.
func DefaultStrings() Strings {
	return Strings{Navigation: "Navigation", Instructions: "Instructions", Models: "Model catalog", Context: "Context", Gears: "Gears", Workspaces: "Workspaces", Variables: "Variables & Secrets", Plugins: "Plugins"}
}

// Action is one control that causes a request.
//
// This is the highest-leverage rule in the model contract. Because actions are
// DATA, a button the host adds later appears inside an override somebody wrote
// last year, instead of the override being the reason nobody ever sees it.
type Action struct {
	ID      string
	Label   string
	Method  string
	Href    string
	Confirm string
	Target  string
	Danger  bool
}

// NavItem is one destination in the rail. A plugin contributes one through its
// manifest's nav list; the host merges it in, so N plugins adding N entries
// all get theirs.
type NavItem struct {
	Label   string
	Icon    string
	Href    string
	Order   int
	Current bool
	// From names the plugin that contributed it, empty for the host's own.
	// Shown nowhere by default; present so an operator's debugging screen can
	// answer "where did this button come from".
	From string
}

// Asset is a stylesheet or module the shell injects.
type Asset struct {
	Src string
	// Integrity is computed from the bundle at install, never declared by an
	// author. A digest somebody types is a digest somebody gets wrong, and one
	// that disagrees with the file is worse than none.
	Integrity string
}

// Shell is the model the document renders against.
type Shell struct {
	Ctx   Ctx
	Title string
	// AppHead is the application's own head, carried through as-is.
	//
	// Vite writes hashed asset names into it on every build, so restating them
	// in a template would be a second place the build output has to be kept
	// true — and the one that drifted would be the one serving a stale bundle.
	// It is template.HTML because it comes from this repository's own build
	// output rather than from anything a request could influence.
	AppHead template.HTML
	// Body is the rendered page. Filled by a two-pass render: the page's own
	// template runs first, and its output is placed here — because Go
	// templates cannot take a template name from data, and the alternative
	// would be a fixed name every page had to be squeezed into.
	Body template.HTML
	// Nav is the rail: the host's own destinations first, then whatever
	// plugins contributed, in the order the manifests asked for.
	//
	// Rendered by the document on pages this shell serves. It is NOT rendered
	// on the application's own screens, which still draw their own — a second
	// rail above the first would be two rails.
	Nav []NavItem
	// Styles and Scripts are plugin contributions injected into the head.
	Styles  []string
	Scripts []Asset
}

// Page is the model a plugin's own page renders against.
//
// Deliberately small. A page that needs more than its context, its title and
// what was in the URL needs a backend, and pretending otherwise would put a
// half-model in front of an author who then discovers the missing half later.
type Page struct {
	Ctx    Ctx
	Title  string
	Params map[string]string
	Query  map[string]string
	// Data is whatever the plugin's provider returned, or nil when the page is
	// templates alone. Deliberately untyped: it is the plugin's own shape, and
	// a host that insisted on knowing it would be a host that has to be
	// changed every time an author adds a field.
	Data any
}

// CoreModels is the zero-value model for every name the host owns.
//
// Registering one here is what puts a name under the compatibility check: a
// plugin that overrides it is executed against this at boot, and a reference
// to a field that no longer exists disables that plugin by name instead of
// rendering a blank region nobody can explain.
func CoreModels() Models {
	shell := Shell{Ctx: Ctx{T: DefaultStrings()}}
	return Models{
		"cog.shell.document": shell,
		"cog.shell.head":     shell,
		"cog.shell.tokens":   shell,
		"cog.shell.rail":     shell,
		"cog.shell.body":     shell,
		"cog.slot.head":      shell,
		"cog.slot.rail":      shell,
		"cog.row.nav":        NavItem{},
		"cog.action.button":  Action{},
		"cog.list.actions":   []Action{},
		"cog.empty.default":  "",
		"cog.page.plugin":    Page{Ctx: Ctx{T: DefaultStrings()}},

		// The library, the first screen of the product served as a template.
		// Four names rather than one: a plugin that only wants a different row
		// should not have to reproduce the page around it.
		"cog.page.instructions":  Instructions{Ctx: Ctx{T: DefaultStrings()}},
		"cog.list.instructions":  Instructions{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.instruction":    Instruction{},
		"cog.empty.instructions": Instructions{Ctx: Ctx{T: DefaultStrings()}},

		// The model catalogue. Providers and the catalogue are separate lists
		// on one page because they are separate decisions: where models come
		// from, and which of them this install offers an agent.
		// The context space: a list of files, a search across them, and one
		// file open for editing.
		// The landing screen.
		// Drawers. A drawer crawls out over the work rather than replacing
		// it, so it is the same list without the page frame — and its own
		// name, because a plugin overriding the drawer should not have to take
		// the page with it.
		"cog.page.plugins":    Plugins{Ctx: Ctx{T: DefaultStrings()}},
		"cog.list.plugins":    Plugins{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.plugin":      PluginRow{},
		"cog.list.names":      NameList{},
		"cog.list.catalog":    Plugins{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.catalogitem": CatalogRow{},
		"cog.empty.plugins":   Plugins{Ctx: Ctx{T: DefaultStrings()}},
		"cog.drawer.terminal": Terminal{Ctx: Ctx{T: DefaultStrings()}},
		"cog.stage.chat":      Transcript{Ctx: Ctx{T: DefaultStrings()}},
		"cog.list.messages":   Transcript{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.message":     ChatMessage{},
		"cog.drawer.memory":   Memory{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.memory":      MemoryItem{},
		"cog.drawer.agents":   Agents{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.agent":       AgentCard{},
		"cog.drawer.mcp":      MCP{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.mcpserver":   MCPServer{},

		"cog.drawer.receivers": Inlets{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.receiver":     Inlet{},

		"cog.page.variables":   Env{Ctx: Ctx{T: DefaultStrings()}},
		"cog.drawer.variables": Env{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.envname":      EnvName{},
		"cog.drawer.queue":     Queue{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.unit":         Unit{},
		"cog.row.schedule":     Schedule{},

		"cog.drawer.instructions": Instructions{Ctx: Ctx{T: DefaultStrings()}},
		"cog.drawer.gears":        Gears{Ctx: Ctx{T: DefaultStrings()}},
		"cog.drawer.context":      Context{Ctx: Ctx{T: DefaultStrings()}},

		"cog.page.workspaces":  Workspaces{Ctx: Ctx{T: DefaultStrings()}},
		"cog.list.workspaces":  Workspaces{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.workspace":    WorkspaceRow{},
		"cog.empty.workspaces": Workspaces{Ctx: Ctx{T: DefaultStrings()}},

		// The gear catalogue. cog.row.gear is the name the documentation uses
		// as its recurring override example, so it has to be small enough to
		// be worth overriding on its own.
		"cog.page.gears":  Gears{Ctx: Ctx{T: DefaultStrings()}},
		"cog.list.gears":  Gears{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.gear":    Gear{},
		"cog.empty.gears": Gears{Ctx: Ctx{T: DefaultStrings()}},

		"cog.page.context":    Context{Ctx: Ctx{T: DefaultStrings()}},
		"cog.list.context":    Context{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.contextfile": ContextFile{},
		"cog.empty.context":   Context{Ctx: Ctx{T: DefaultStrings()}},

		"cog.page.models":    ModelCatalog{Ctx: Ctx{T: DefaultStrings()}},
		"cog.list.providers": ModelCatalog{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.provider":   Provider{},
		"cog.list.models":    ModelCatalog{Ctx: Ctx{T: DefaultStrings()}},
		"cog.row.model":      Model{},
		"cog.empty.models":   ModelCatalog{Ctx: Ctx{T: DefaultStrings()}},
	}
}

// Palette is the colours a workspace can be given.
//
// The same ten the application offers, here so a server-rendered screen and a
// client-rendered one cannot drift into two palettes. Ten rather than a
// spectrum: a colour is for telling two workspaces apart at a glance, and a
// picker with every hue in it makes that harder rather than easier.
var Palette = []int{264, 166, 212, 328, 44, 18, 140, 292, 190, 350}

// WorkspaceTeam is one team a workspace is shared with.
type WorkspaceTeam struct {
	ID   int64
	Name string
}

// WorkspaceRow is one group of agents behind an orchestrator, as a row.
//
// Not called Workspace: that name is the one in scope on Ctx, which is a
// different thing with two fields and a different lifetime. Two Workspaces in
// one package is two things somebody disambiguates at every use.
type WorkspaceRow struct {
	ID          int64
	Name        string
	Description string
	// Hue is the colour, in degrees. The palette mixes every neutral towards
	// it, so what is stored is the angle rather than one chosen swatch.
	Hue    int
	HasHue bool
	// Mine says the viewer owns it, and SharedWithMe is its negation as a
	// field rather than a {{if not}} — the template function set is small on
	// purpose and `not` is not in it, because a template renders rather than
	// reasons.
	Mine         bool
	SharedWithMe bool
	// MayDelete is the owner or an administrator. A workspace somebody else
	// owns is not yours to remove.
	MayDelete bool
	MayShare  bool
	// Shared is every team it has gone to. A list rather than a picker: a
	// workspace can go to any number of teams, and each is withdrawn on its
	// own rather than by replacing whoever currently has it.
	Shared []WorkspaceTeam
	// Teams are the ones it could still go to, for the picker.
	Teams []WorkspaceTeam
	// Palette is the colours on offer, each already knowing whether it is the
	// one in effect. Clearing is not choosing grey: it hands the workspace
	// back the colour derived from its id and records that nobody picked.
	Palette []Hue
}

// Hue is one colour on offer.
type Hue struct {
	Degrees int
	Chosen  bool
}

// Workspaces is what the landing screen renders against.
type Workspaces struct {
	Ctx   Ctx
	Items []WorkspaceRow
	// Models are what an orchestrator can be given. Without one there is
	// nothing to think with, and the form says so rather than offering an
	// empty list.
	Models   []Model
	Creating bool
	// Importing is the bundle form, open. Its own state rather than a second
	// page: importing is how a workspace arrives from somewhere else, and it
	// belongs beside the list it will appear in.
	Importing bool
	Error     string
	Notice    string
}

// PluginRow is one installed plugin, as the library screen draws it.
//
// Everything under the name is computed by the server from what a plugin
// actually ships, never from what its manifest claimed — so nothing here can
// be improved by writing a nicer manifest.
type PluginRow struct {
	ID      string
	Name    string
	Version string
	Docs    string
	Source  string

	// Readable is false only when the directory could not be read as a plugin
	// at all. That is a different thing from a plugin whose templates failed,
	// and conflating them costs an operator the ability to switch a
	// still-installed plugin back on.
	Readable bool
	// Pending is why it may not be enabled yet, empty when it may. Installing
	// is not a decision; approval is, and it covers exact content.
	Pending    string
	ApprovedBy string
	ApprovedAt string
	Dev        bool

	Enabled bool
	Order   int
	// Live is whether it is actually rendering. Enabled and live are different
	// questions, and the gap between them is the reason this screen exists.
	Live    bool
	Problem string
	State   string
	// StateTone is "ok", "warn", "danger" or empty — which of the four the
	// badge is, decided here rather than by a template comparing strings.
	StateTone string

	Tier      string
	Available bool
	Refusal   string

	// Names is what this plugin does to the template stack and what it asks
	// for, already labelled and in the order somebody reads them.
	//
	// A list of lists rather than six fields and six ifs in the template: the
	// first draft wanted a helper function for that, and the function set is a
	// permanent promise to every plugin author — so the assembling happens
	// where assembling belongs.
	Names []NameList

	Pages   []PluginPageRow
	Hosts   []string
	Secrets []string
	API     []string

	CanMoveUp bool
}

// NameList is one labelled group of template names on a plugin's card.
type NameList struct {
	Label string
	Names []string
	// Tone is "warn" for the two groups that are news rather than description:
	// a name overridden without saying so, and one that renders nothing.
	Tone string
}

// PluginPageRow is one page a plugin serves.
type PluginPageRow struct {
	Path  string
	Title string
	Auth  string
	// Live decides whether the path is a link: a page from a plugin that is
	// not rendering goes nowhere.
	Live bool
}

// CatalogRow is one entry in the shared catalog.
type CatalogRow struct {
	ID          string
	Name        string
	Author      string
	Description string
	Source      string
	Version     string

	Installed        bool
	InstalledVersion string
	Update           bool

	// Verified is three states rather than a badge, because a badge that
	// survives a version change is a badge about a name rather than about
	// code.
	VerifiedRead  bool
	VerifiedOther bool
	Unchecked     bool
	VerifiedAt    string
	VerifiedBy    string
	VerifiedNote  string
}

// Plugins is what the library screen renders against.
type Plugins struct {
	Ctx Ctx
	// Library is true when the catalog half is showing rather than what is
	// installed. Two views of one question, on one screen.
	Library bool

	Items    []PluginRow
	Query    string
	Sort     string
	Narrowed bool

	Catalog       []CatalogRow
	CatalogQuery  string
	CatalogTotal  int
	Cached        bool
	Fetched       string
	Versioned     bool
	Updates       []CatalogUpdateRow
	CatalogFailed string

	// RestartOwed is sticky once true. A restart is owed until it happens, and
	// somebody who enables three plugins should not watch the reminder vanish
	// because the last of the three happened to change nothing.
	RestartOwed bool
	CanRestart  bool
	Error       string
	Notice      string
}

// CatalogUpdateRow is one installed plugin the catalog offers a newer version
// of.
type CatalogUpdateRow struct {
	Name      string
	Installed string
	Available string
}

// Terminal is the gate around a shell, which is all of a terminal a template
// can render.
//
// The session itself is a live PTY over a socket: bytes in both directions,
// redrawn by the client sixty times a second. A template renders a thing that
// exists at a moment, and a terminal is the opposite of that. What IS
// renderable is everything around it — why there is or is not one, what
// starting it costs, and what closing it loses — and that is the half somebody
// reads before deciding.
type Terminal struct {
	Ctx Ctx
	// Available is whether this install offers one at all, and Reason says why
	// not in the words the server already uses.
	Available bool
	Reason    string
	// Started is whether the client has a session open, in which case the gate
	// stands aside.
	Started bool
}

// Attachment is a file that travelled with a message.
//
// Shown on the message rather than in a panel somewhere: what was attached is
// part of what was said, and a conversation where the operator's file is
// invisible is one where nobody can tell why the answer talks about a diagram.
type Attachment struct {
	Name  string
	Bytes string
	// ToGear marks a file the model cannot read, which goes to a gear as a
	// path instead. Saying so is the difference between "it ignored my file"
	// and "it handed it on".
	ToGear bool
	Title  string
}

// ToolCall is one tool an assistant turn asked for.
type ToolCall struct{ Name string }

// ChatMessage is one entry in the transcript.
type ChatMessage struct {
	ID   int64
	Kind string
	// Who is the agent's name, and Named says whether to print it. Only a
	// delegate is named: the orchestrator is the voice of the workspace, so
	// labelling every one of its replies is the same noise as labelling your
	// own.
	Who   string
	Named bool

	Content string
	At      string

	Attachments []Attachment
	Calls       []ToolCall
	// ToolName and ToolFailed describe a tool result, which renders folded.
	ToolName   string
	ToolFailed bool

	// Which shape this row takes, as fields rather than a comparison in the
	// template.
	//
	// The alternative was a compare function, and the test that guards the
	// function set was right to refuse it: every name in that set is a
	// permanent promise to every plugin author, and a general `eq` is the one
	// that turns templates into programs. A template renders; deciding which
	// of five shapes a message is belongs to whoever already knows its kind.
	IsUser       bool
	IsAssistant  bool
	IsToolResult bool
	IsDelegation bool
}

// Transcript is the conversation, as it is replayed to the model.
type Transcript struct {
	Ctx      Ctx
	Messages []ChatMessage
	// Empty is a whole screen rather than a line, so it gets its own state
	// instead of an if around a paragraph.
	Empty bool
	Error string
}

// MemoryItem is one thing an agent carries into every turn.
type MemoryItem struct {
	// Label is what this piece IS, in the words somebody reads: "role",
	// "private to this agent", "shared with the workspace", "bound document",
	// "from the library".
	Label       string
	Kind        string
	Source      string
	Description string
	Content     string
	// Editable and Removable decide which controls the row carries. A role is
	// neither: it is what the agent is, and dropping it would leave an agent
	// with no answer to what it is for.
	Editable  bool
	Removable bool
	IsRole    bool
	// Editing marks the row somebody opened, so the panel comes back with the
	// draft still open.
	Editing bool
	// BindingID is set when forgetting means unbinding: the document itself
	// stays, and only this agent stops reading it.
	BindingID int64
	// Version is what this text was read at, carried back on a save or a
	// delete. Removing a document somebody has just rewritten is exactly as
	// destructive as overwriting it, so both carry it.
	Version string
}

// Memory is everything one agent remembers, in the order it reaches the model.
//
// The point is not tidiness. An agent that quietly carries something it picked
// up once will keep steering by it, and the only way to stop that is to be
// able to see it — so nothing here is behind a summary: the text is the text,
// and next to it is the way to change or drop it.
type Memory struct {
	Ctx       Ctx
	AgentID   int64
	AgentName string
	Items     []MemoryItem
	Error     string
}

// AgentCard is one agent, as the roster draws it.
type AgentCard struct {
	ID   int64
	Name string
	// Orchestrator marks the workspace entry point.
	Orchestrator bool
	Model        string
	// State is "idle" or "working", and Detail is what it is doing when it is.
	State  string
	Detail string
	// Spend is what this agent has cost, already in the words the card shows:
	// empty before it has run, "n/a" when the provider reported nothing, and a
	// compact number otherwise. A confident 0 for a provider that reports
	// nothing would be a lie the operator finds out about from a bill.
	Spend string
	// SpendDetail is the whole story, for a title somebody hovers.
	SpendDetail string
	// Share is this agent's percentage of the WORKSPACE's spend — its share,
	// not a percentage of an invented budget. A number nobody can act on is
	// decoration; a share tells you where the money went.
	Share    int
	HasSpend bool
	Selected bool
}

// Agents is what the roster renders against.
type Agents struct {
	Ctx   Ctx
	Items []AgentCard
	// PollURL is where the panel refreshes itself from, carrying the selected
	// row so a refresh does not clear the selection under whoever is reading
	// it. Built by the server rather than in the template: a URL assembled
	// from three fields inside markup is a URL somebody gets wrong once and
	// nobody notices, because a poll that 404s just stops refreshing.
	PollURL string
	Error   string
}

// MCPTool is one capability a server offers, approved on its own.
//
// One at a time, because a server's tool list is its own to change: approving
// a server is not approving whatever it decides to offer next week.
type MCPTool struct {
	ID          int64
	Name        string
	Description string
	Approved    bool
}

// MCPServer is somebody else's tools, granted to an agent.
type MCPServer struct {
	ID   int64
	Name string
	// Kind is "packaged" or "hosted", and the two are genuinely different
	// risks — saying the same four things about both would be false in two of
	// them.
	Kind     string
	Hosted   bool
	Command  string
	Address  string
	Status   string
	Approved bool
	// Secrets are the names it is handed, resolved at spawn and belonging to a
	// real account with that account's permissions.
	Secrets []string
	Tools   []MCPTool
	// ApprovedTools and TotalTools are the count somebody reads before opening
	// the list.
	ApprovedTools int
	TotalTools    int
}

// MCP is what the MCP drawer renders against.
type MCP struct {
	Ctx Ctx
	// Enabled is false when this install was not asked for external servers.
	// Off is the default and the default is the point.
	Enabled bool
	IsAdmin bool
	Servers []MCPServer
	Error   string
	Notice  string
}

// InletTask is one thing a receiver can be asked to do.
type InletTask struct {
	Name string
	// Agent is who does the work, by name — the pair an operator reads.
	Agent       string
	Accepts     string
	Instruction string
	// CallbackURL is where this install posts the finished run. Empty is the
	// ordinary case: the answer goes back on the caller's own connection.
	CallbackURL string
}

// Inlet is one door into a workspace.
type Inlet struct {
	ID          int64
	Address     string
	Description string
	// HasKey is whether a key was issued — never the key. It is shown once, in
	// the response that issues it, and only its hash is kept.
	HasKey     bool
	IssuedAt   string
	LastUsedAt string
	Tasks      []InletTask
	// JustIssued carries a key somebody just asked for. This is the only time
	// it appears anywhere.
	JustIssued string
}

// InletRun is one delivery, recorded before the work started.
type InletRun struct {
	Task   string
	State  string
	Failed bool
	Agent  string
	At     string
	Error  string
}

// Inlets is what the receivers drawer renders against.
type Inlets struct {
	Ctx    Ctx
	Items  []Inlet
	Runs   []InletRun
	Agents []string
	Error  string
	Notice string
}

// EnvName is one named value a gear can be given.
type EnvName struct {
	Name string
	// Kind is "variable" or "secret". A variable's value is shown afterwards;
	// a secret's is not, and the difference is the whole reason the two are
	// separate words rather than a flag.
	Kind        string
	Value       string
	Secret      bool
	Description string
	// Source names where this value came from, because a name can be supplied
	// by this workspace, by the install, or by a file on disk — and somebody
	// whose value is not the one they set has to be able to see which.
	Source string
	// FromWorkspace marks a value this workspace set, which wins over the same
	// name set install-wide.
	FromWorkspace bool
}

// Env is what the variables drawer renders against.
type Env struct {
	Ctx   Ctx
	Names []EnvName
	// Workspace is set when this is a workspace's own list rather than the
	// install's.
	Workspace bool
	// CanStoreSecrets is false without a secret key, and then a secret cannot
	// be stored at all rather than being stored badly.
	CanStoreSecrets bool
	VariablesDir    string
	SecretsDir      string
	HasDirs         bool
	// JustSet is a name somebody just wrote. If it was a secret this is the
	// only time it appears anywhere.
	JustSet string
	Error   string
}

// Unit is one piece of work on the queue.
type Unit struct {
	ID    int64
	Kind  string
	State string
	Lane  string
	// Attempts and MaxAttempts say how close this is to being abandoned.
	Attempts    int
	MaxAttempts int
	RunAfter    string
	LastError   string
	Failed      bool
}

// Schedule is one clock.
type Schedule struct {
	ID      int64
	Name    string
	Spec    string
	TZ      string
	Enabled bool
	Target  string
	NextRun string
	LastRun string
	// LastError is what went wrong last time it fired, which is the question
	// somebody opens this drawer with.
	LastError string
}

// Queue is what the queue drawer renders against.
type Queue struct {
	Ctx       Ctx
	Units     []Unit
	Schedules []Schedule
	Error     string
	Notice    string
}

// GearFile is one file of a gear's source.
type GearFile struct {
	Path    string
	Content string
	// Binary marks a file that cannot be read before approving it. Showing
	// megabytes of base64 helps nobody, and pretending a blob is reviewable
	// source would be worse.
	Binary bool
	// KB is its size when it is binary, for the sentence that says so.
	KB int
	// Entrypoint marks the file the gear starts at, which is the one worth
	// opening first.
	Entrypoint bool
	// Diff is what changed since the version an approval last covered, when
	// somebody asked to see it. An approval covers exact content, so the
	// question at review time is not "what is this code" but "what is
	// different from the version I already read".
	Diff []DiffLine
	// TooBig says the pair was past the ceiling and not compared — said out
	// loud, because an empty diff reads as "nothing changed".
	TooBig bool
}

// GearGrant is one named value a gear is given, and where it comes from.
type GearGrant struct {
	Name string
	// Found is whether anything on this install supplies it. Learning this at
	// approval beats learning it at three in the morning from a run that was
	// refused.
	Found  bool
	Kind   string
	Source string
}

// GearApproval is one entry in the trail.
type GearApproval struct {
	Version int
	By      string
	At      string
	Network string
	Timeout string
}

// Gear is one tool an agent forged.
type Gear struct {
	ID          int64
	Name        string
	Description string
	Tags        []string
	Version     int
	// Status is "pending", "approved" or "disabled" — the word an operator
	// reads rather than a code they look up.
	Status     string
	Approved   bool
	Runtime    string
	Entrypoint string

	// Open is whether this row is expanded. Everything below it is filled only
	// then: a list carrying every gear's source would carry every gear's
	// source to draw a page of names.
	Open       bool
	Files      []GearFile
	ArgsSchema string
	Grants     []GearGrant
	// NetworkOn and Hosts are what approving WOULD grant rather than what it
	// granted: the allowlist is set in the same act as the approval, beside
	// the source.
	NetworkOn bool
	Hosts     string
	Timeout   int
	Approvals []GearApproval
	// Comparing is whether the source is being shown as a comparison, and
	// PreviousVersion is what against.
	Comparing       bool
	PreviousVersion int

	// Runs is what this gear has actually done, and Connections is where it
	// actually reached. Both are the record rather than the intention, which
	// is the half of an approval decision the source cannot show.
	Runs        []GearRun
	Connections []GearConnection

	// DryArgs, DryOutput and DryFailed carry a dry run somebody just asked
	// for. A dry run executes the gear NOW, even while pending, so that
	// approval is an informed decision rather than a reading of source.
	DryArgs   string
	DryRan    bool
	DryOutput string
	DryFailed bool
}

// GearRun is one recorded execution.
type GearRun struct {
	At       string
	ExitCode int
	Failed   bool
	Output   string
}

// GearConnection is one destination a gear actually reached, or was refused.
type GearConnection struct {
	Host    string
	Port    int
	Method  string
	State   string
	Refused bool
	At      string
}

// Gears is what the gear catalogue renders against.
type Gears struct {
	Ctx   Ctx
	Items []Gear
	// SandboxKnown and Sandboxed are three states, not two: unknown is not the
	// same as no. Reporting unknown as no would understate the protection on a
	// server that has a sandbox and overstate the danger on one that does not.
	SandboxKnown bool
	Sandboxed    bool
	// Pending is how many are awaiting review, which is usually why somebody
	// opened this screen.
	Pending int
	// IsAdmin gates the controls that change what runs. A member sees the
	// source and cannot approve it.
	IsAdmin bool

	Query     string
	Tag       string
	Tags      []Tag
	Narrowed  bool
	Authoring bool
	Error     string
	Notice    string
}

// ContextFile is one file in the space.
type ContextFile struct {
	Path    string
	Version string
	// Selected marks the one being viewed, so a row knows which of its two
	// shapes it is drawing.
	Selected bool
}

// ContextHit is one line a search matched.
type ContextHit struct {
	Path string
	Line int
	Text string
}

// Context is what the context page renders against.
type Context struct {
	Ctx Ctx
	// Available is whether contextd can be reached at all. When it cannot,
	// nothing else here is meaningful and the page says so instead of drawing
	// an empty space that looks like an empty space.
	Available bool
	Unusable  string
	SpaceRoot string

	Files []ContextFile

	// Open is the file being viewed, empty when none is.
	Open string
	// OpenedAt is the version this text was read at. It travels back with a
	// save so a write that would clobber somebody else's is refused rather
	// than performed — the version is the whole point of the field.
	OpenedAt string
	Text     string

	// Matches is how many lines matched, counted here rather than in the
	// template. The function set a template may call is small on purpose and
	// every name in it is a permanent promise, so a count belongs to whoever
	// already has the slice.
	Matches int
	// Query, Hits and the counts describe a search somebody just ran.
	Searched     bool
	Query        string
	Hits         []ContextHit
	FilesScanned int
	FilesMatched int
	// Truncated says the answer was cut. A cut answer that does not say so
	// reads as "there is nothing else", which is the one wrong answer a search
	// can give.
	Truncated bool

	Error  string
	Notice string
}

// Provider is one place models come from.
type Provider struct {
	ID   int64
	Name string
	// Kind is "anthropic" or "openai-compatible". The word an operator reads,
	// not an enum they have to look up.
	Kind    string
	BaseURL string
	// HasKey is whether a credential is stored — never the credential. A
	// template that could render a key is a template somebody will.
	HasKey bool
	// Models are this provider's entries in the catalogue.
	Models []Model
	// Tested and TestError report the last connection check, when one was
	// made in this request. A check is an action somebody took, so its result
	// belongs to the render that answered it rather than to the row forever.
	Tested    bool
	TestOK    bool
	TestError string
	// Offers is what the provider said it has, when a check succeeded.
	Offers []string
}

// Model is one entry in the catalogue.
type Model struct {
	ID       int64
	Name     string
	Label    string
	Provider string
	Kind     string
}

// ModelCatalog is what the model catalog page renders against.
//
// Not called Models: that name is already the registry of template models in
// this package, and two things called Models in one package is two things
// somebody has to disambiguate at every use. Not called Catalogue either —
// internal/catalog is this, plugin.Catalog is the plugin index, and a third
// bare "catalogue" would be the word that stops meaning anything.
type ModelCatalog struct {
	Ctx       Ctx
	Providers []Provider
	Models    []Model
	Error     string
}

// Instruction is one entry in the library, as a template sees it.
//
// Its own type rather than the store's, because a template model is a promise
// to every plugin that overrides the row: adding a field is free, and removing
// one somebody wrote against is a plugin that disables itself at boot. What is
// here is what a row needs to render, and nothing that happens to be in the
// database beside it.
type Instruction struct {
	ID          int64
	Name        string
	Description string
	Tags        []string
	// Text is the body, present only when this row is the one being read. A
	// list that carried every body would carry every version of every
	// instruction in the library to draw a page of names.
	Text string
	// Open says this row is expanded, so an override knows which of the two
	// shapes it is drawing.
	Open      bool
	UpdatedAt string
}

// Tag is one filter option.
type Tag struct {
	Name     string
	Selected bool
}

// Instructions is the model the library page renders against.
type Instructions struct {
	Ctx   Ctx
	Items []Instruction
	// Query and Tag are what the list was narrowed by, echoed back so the
	// controls show what is in effect rather than resetting on every render.
	Query string
	Tag   string
	// Tags is every tag in the library, for the filter, each already knowing
	// whether it is the one in effect. Computed here rather than compared in
	// the template: which option is selected is a fact about the request, and
	// a template working it out is a template that has to be given the request
	// to work it out from.
	Tags []Tag
	// Narrowed says a search or a tag is in effect, so the empty state can say
	// "nothing matches that" rather than "the library is empty" — two
	// sentences that mean opposite things to the person reading them.
	//
	// Computed here for the same reason Tag.Selected is: a template renders,
	// it does not decide. The function set every template may call is small on
	// purpose, and every name in it is a permanent promise to every plugin.
	Narrowed bool

	// Error is what went wrong with the last thing somebody did, in the words
	// they need. Empty is the ordinary case.
	Error string
}

// HostNav is the product's own rail, as the server knows it.
//
// Here rather than only in the client so a page this shell serves looks like
// the product rather than like a bare document dropped beside it. The client
// keeps its own copy for its own screens; this is the same list, and the two
// meeting in the middle is what the conversion is for.
//
// Order is spaced by hundreds so a plugin can land between two of them without
// anybody renumbering.
func HostNav(current string) []NavItem {
	items := []NavItem{
		{Label: "Workspaces", Href: "/workspaces", Icon: "grid", Order: 100},
		{Label: "Map", Href: "/map", Icon: "map", Order: 200},
		{Label: "Gears", Href: "/gears", Icon: "gear", Order: 300},
		{Label: "Models", Href: "/models", Icon: "model", Order: 400},
		{Label: "Instructions", Href: "/instructions", Icon: "text", Order: 500},
		{Label: "Context", Href: "/context", Icon: "layers", Order: 600},
		{Label: "Plugins", Href: "/plugins", Icon: "plug", Order: 700},
	}
	for i := range items {
		// Prefix rather than equality: /workspaces/4 is still Workspaces, and
		// a rail that forgets where you are the moment you open something is
		// a rail that is wrong on most of the screens anybody uses.
		items[i].Current = current == items[i].Href ||
			(items[i].Href != "/" && strings.HasPrefix(current, items[i].Href+"/"))
	}
	return items
}

// Exemplars are the same models with something in them.
//
// The zero-value pass catches a template naming a field that does not exist,
// which is the failure that disables a plugin at boot. It cannot catch the
// other one: a body that renders emptily because every value it reaches for is
// blank, and reads on screen as a plugin that did nothing. Ranging over an
// empty slice succeeds, so a template whose entire content is inside a
// {{range}} passes the zero pass while producing not one byte.
//
// So there are two passes. Zero says "this will not crash"; exemplar says
// "this will put something on screen", and gives the approval screen something
// to render as a preview — which is what turns "it overrides cog.row.nav" into
// a picture of what that will look like.
func Exemplars() Models {
	ctx := Ctx{
		Lang: "en", T: DefaultStrings(),
		Viewer: Viewer{ID: 1, Name: "sam", SignedIn: true, IsAdmin: true},
	}
	shell := Shell{
		Ctx: ctx, Title: "Example",
		Body: "<p>Whatever the page rendered goes here.</p>",
		Nav: []NavItem{
			{Label: "Workspaces", Href: "/workspaces", Icon: "grid", Order: 100},
			{Label: "Gears", Href: "/gears", Icon: "gear", Order: 200, Current: true},
		},
		Styles:  []string{"/p/example/example.css"},
		Scripts: []Asset{{Src: "/p/example/example.js"}},
	}
	actions := []Action{
		{ID: "approve", Label: "Approve", Method: "POST", Href: "/api/v1/example/approve"},
		{ID: "remove", Label: "Remove", Method: "DELETE", Href: "/api/v1/example", Danger: true},
	}
	return Models{
		"cog.shell.document": shell,
		"cog.shell.head":     shell,
		"cog.shell.tokens":   shell,
		"cog.shell.rail":     shell,
		"cog.shell.body":     shell,
		"cog.slot.head":      shell,
		"cog.slot.rail":      shell,
		"cog.row.nav":        shell.Nav[1],
		"cog.action.button":  actions[0],
		"cog.list.actions":   actions,
		"cog.empty.default":  "Nothing here yet.",
		"cog.page.plugin": Page{
			Ctx: ctx, Title: "Example",
			Params: map[string]string{"id": "42"},
			Query:  map[string]string{"q": "search"},
		},

		"cog.page.instructions":  exampleLibrary(ctx),
		"cog.list.instructions":  exampleLibrary(ctx),
		"cog.empty.instructions": Instructions{Ctx: ctx, Query: "nothing matches this"},
		"cog.row.instruction":    exampleLibrary(ctx).Items[0],

		"cog.page.models":    exampleModels(ctx),
		"cog.list.providers": exampleModels(ctx),
		"cog.list.models":    exampleModels(ctx),
		"cog.row.provider":   exampleModels(ctx).Providers[0],
		"cog.row.model":      exampleModels(ctx).Models[0],
		"cog.empty.models":   ModelCatalog{Ctx: ctx},

		"cog.page.plugins": examplePlugins(ctx),
		"cog.list.plugins": examplePlugins(ctx),
		"cog.row.plugin":   examplePlugins(ctx).Items[0],
		"cog.list.names":   NameList{Label: "Overrides", Names: []string{"cog.row.gear"}},
		"cog.list.catalog": examplePlugins(ctx),
		"cog.row.catalogitem": CatalogRow{ID: "release-radar", Name: "Release Radar",
			Author: "someone", Description: "Watches releases.", Unchecked: true},
		"cog.empty.plugins": Plugins{Ctx: ctx},

		"cog.drawer.terminal": Terminal{Ctx: ctx, Available: true},

		"cog.stage.chat": Transcript{Ctx: ctx, Messages: []ChatMessage{
			{ID: 1, Kind: "user", IsUser: true, Content: "Cut a release from main."},
			{ID: 2, Kind: "assistant", IsAssistant: true, Content: "Reading the commits since v2.0.0.",
				Calls: []ToolCall{{Name: "run_gear"}}},
			{ID: 3, Kind: "tool_result", IsToolResult: true, ToolName: "run_gear",
				Content: `{"notes": ["fix the gate"]}`},
		}},
		"cog.list.messages": Transcript{Ctx: ctx, Messages: []ChatMessage{
			{ID: 1, Kind: "user", IsUser: true, Content: "Cut a release from main."},
		}},
		"cog.row.message": ChatMessage{ID: 2, Kind: "assistant", IsAssistant: true, Who: "Reviewer", Named: true,
			Content: "The diff is empty, so there is nothing to review."},

		"cog.drawer.memory": Memory{Ctx: ctx, AgentID: 2, AgentName: "Reviewer",
			Items: []MemoryItem{
				{Label: "role", Kind: "role", IsRole: true, Source: "role",
					Content: "Reads the diff and refuses it out loud."},
				{Label: "private to this agent", Kind: "private", Source: "agents/reviewer/notes.md",
					Description: "what it learned", Content: "The build tags matter on darwin.",
					Editable: true},
			}},
		"cog.row.memory": MemoryItem{Label: "bound document", Kind: "bound",
			Source: "decisions.md", Removable: true, BindingID: 4},

		"cog.drawer.agents": Agents{Ctx: ctx, Items: []AgentCard{
			{ID: 1, Name: "orchestrator", Orchestrator: true, Model: "Opus", State: "working",
				Detail: "thinking", Spend: "12.4k", SpendDetail: "9,100 in + 3,300 out",
				Share: 62, HasSpend: true, Selected: true},
			{ID: 2, Name: "Reviewer", Model: "Qwen Coder", State: "idle", Spend: "7.6k",
				SpendDetail: "5,000 in + 2,600 out", Share: 38, HasSpend: true},
		}},
		"cog.row.agent": AgentCard{ID: 2, Name: "Reviewer", Model: "Qwen Coder", State: "idle"},

		"cog.drawer.mcp": MCP{Ctx: ctx, Enabled: true, IsAdmin: true,
			Servers: []MCPServer{{ID: 1, Name: "jira", Kind: "packaged",
				Command: "npx -y @atlassian/mcp", Status: "approved", Approved: true,
				Secrets: []string{"JIRA_TOKEN"}, ApprovedTools: 2, TotalTools: 7,
				Tools: []MCPTool{{ID: 1, Name: "search_issues", Approved: true},
					{ID: 2, Name: "create_issue"}}}}},
		"cog.row.mcpserver": MCPServer{ID: 1, Name: "jira", Kind: "packaged", Status: "approved"},

		"cog.drawer.receivers": Inlets{Ctx: ctx, Agents: []string{"orchestrator", "Triage"},
			Items: []Inlet{{ID: 1, Address: "helpdesk", Description: "Where the helpdesk posts a ticket",
				HasKey: true, IssuedAt: "2026-08-19",
				Tasks: []InletTask{{Name: "classify", Agent: "orchestrator", Accepts: "json",
					Instruction: "Answer with one of: platform, research, billing."}}}}},
		"cog.row.receiver": Inlet{ID: 1, Address: "helpdesk", HasKey: true},

		"cog.page.variables": Env{Ctx: ctx, CanStoreSecrets: true,
			Names: []EnvName{{Name: "REGION", Kind: "variable", Value: "eu-central-1", Source: "the install"}}},
		"cog.drawer.variables": Env{Ctx: ctx, Workspace: true, CanStoreSecrets: true,
			Names: []EnvName{
				{Name: "REGION", Kind: "variable", Value: "eu-central-1", Source: "this workspace", FromWorkspace: true},
				{Name: "DEPLOY_TOKEN", Kind: "secret", Secret: true, Source: "the install"},
			}},
		"cog.row.envname": EnvName{Name: "REGION", Kind: "variable", Value: "eu-central-1"},
		"cog.drawer.queue": Queue{Ctx: ctx,
			Units: []Unit{{ID: 4, Kind: "chat", State: "claimed", Lane: "ws:1", MaxAttempts: 1}},
			Schedules: []Schedule{{ID: 1, Name: "morning sweep", Spec: "every 15m",
				TZ: "Europe/Berlin", Enabled: true, Target: "the orchestrator"}}},
		"cog.row.unit":     Unit{ID: 4, Kind: "chat", State: "queued", Lane: "ws:1", MaxAttempts: 1},
		"cog.row.schedule": Schedule{ID: 1, Name: "morning sweep", Spec: "every 15m", Enabled: true},

		"cog.drawer.instructions": exampleLibrary(ctx),
		"cog.drawer.gears":        exampleGears(ctx),
		"cog.drawer.context":      exampleContext(ctx),

		"cog.page.workspaces":  exampleWorkspaces(ctx),
		"cog.list.workspaces":  exampleWorkspaces(ctx),
		"cog.row.workspace":    exampleWorkspaces(ctx).Items[0],
		"cog.empty.workspaces": Workspaces{Ctx: ctx},

		"cog.page.gears":  exampleGears(ctx),
		"cog.list.gears":  exampleGears(ctx),
		"cog.row.gear":    exampleGears(ctx).Items[0],
		"cog.empty.gears": Gears{Ctx: ctx},

		"cog.page.context":    exampleContext(ctx),
		"cog.list.context":    exampleContext(ctx),
		"cog.row.contextfile": exampleContext(ctx).Files[0],
		"cog.empty.context":   Context{Ctx: ctx, Available: true},
	}
}

func exampleWorkspaces(ctx Ctx) Workspaces {
	teams := []WorkspaceTeam{{ID: 1, Name: "Platform"}, {ID: 2, Name: "Research"}}
	return Workspaces{
		Ctx: ctx,
		Items: []WorkspaceRow{
			{ID: 1, Name: "Release engineering", Description: "Cuts releases and checks them",
				Hue: 225, HasHue: true, Mine: true, MayDelete: true, MayShare: true,
				Shared: teams, Teams: teams},
			{ID: 2, Name: "Support triage", Description: "Reads what arrives and files it",
				Hue: 25, HasHue: true, MayShare: true, Teams: teams},
		},
		Models: []Model{{ID: 1, Name: "claude-opus-4", Label: "Opus", Provider: "Anthropic"}},
	}
}

func examplePlugins(ctx Ctx) Plugins {
	return Plugins{
		Ctx: ctx, CanRestart: true,
		Items: []PluginRow{
			{ID: "pulse", Name: "Pulse", Version: "1.0.0", Readable: true, Enabled: true,
				Order: 1, Live: true, State: "live", StateTone: "ok", Tier: "bundle",
				Available: true, ApprovedBy: "admin", ApprovedAt: "2026-08-19",
				Names: []NameList{{Label: "Adds", Names: []string{"pulse.page.panel"}}},
				Pages: []PluginPageRow{{Path: "/p/pulse/panel", Auth: "token", Live: true}}},
			{ID: "radar", Name: "Release Radar", Version: "2.4.0", Readable: true,
				State: "needs approval", StateTone: "warn", Tier: "wasm", Available: true,
				Pending: "nobody has approved this plugin on this install yet"},
		},
		Catalog: []CatalogRow{{ID: "release-radar", Name: "Release Radar", Author: "someone",
			Description: "Watches releases and files them into a workspace.", Unchecked: true}},
		CatalogTotal: 1,
	}
}

func exampleGears(ctx Ctx) Gears {
	return Gears{
		Ctx: ctx, SandboxKnown: true, Sandboxed: true, Pending: 1, IsAdmin: true,
		Items: []Gear{
			{ID: 1, Name: "changelog", Description: "Turns a range of commits into release notes",
				Tags: []string{"release", "git"}, Version: 3, Status: "approved", Approved: true,
				Runtime: "python", Entrypoint: "main.py"},
			{ID: 2, Name: "fetch_manifest", Description: "Reads the published manifest for a release",
				Tags: []string{"release", "http"}, Version: 1, Status: "pending",
				Runtime: "python", Entrypoint: "main.py"},
		},
		Tags: []Tag{{Name: "git"}, {Name: "http"}, {Name: "release"}},
	}
}

func exampleContext(ctx Ctx) Context {
	return Context{
		Ctx: ctx, Available: true, SpaceRoot: "~/.contextverse/spaces/solo",
		Files: []ContextFile{
			{Path: "instructions/house-voice.md", Version: "v3", Selected: true},
			{Path: "notes/release-2.md", Version: "v1"},
		},
		Open: "instructions/house-voice.md", OpenedAt: "v3", Matches: 2,
		Text: "Short sentences. No adjective you cannot defend.\n",
	}
}

func exampleModels(ctx Ctx) ModelCatalog {
	catalogue := []Model{
		{ID: 1, Name: "claude-opus-4", Label: "Opus — the one that thinks",
			Provider: "Anthropic", Kind: "anthropic"},
		{ID: 2, Name: "qwen2.5-coder:14b", Label: "Qwen Coder — local, free",
			Provider: "Ollama", Kind: "openai-compatible"},
	}
	return ModelCatalog{
		Ctx: ctx,
		Providers: []Provider{
			{ID: 1, Name: "Anthropic", Kind: "anthropic",
				BaseURL: "https://api.anthropic.com", HasKey: true, Models: catalogue[:1]},
			{ID: 2, Name: "Ollama", Kind: "openai-compatible",
				BaseURL: "http://127.0.0.1:11434/v1", Models: catalogue[1:]},
		},
		Models: catalogue,
	}
}

func exampleLibrary(ctx Ctx) Instructions {
	return Instructions{
		Ctx: ctx,
		Items: []Instruction{
			{ID: 1, Name: "house-voice", Description: "How anything written here should read",
				Tags: []string{"writing"}, UpdatedAt: "2026-08-19"},
			{ID: 2, Name: "refuse-an-empty-diff", Description: "A review of nothing is not a review",
				Tags: []string{"review"}, UpdatedAt: "2026-08-18"},
		},
		Tags: []Tag{{Name: "review"}, {Name: "writing", Selected: true}},
	}
}

// Funcs is the function set every template may call, host and plugin alike.
//
// Kept small on purpose. Every name here is a permanent promise to every
// plugin author, and a function is much harder to withdraw than a template
// body — a body can be replaced, but a call that no longer resolves is a parse
// error that takes the whole plugin down.
func Funcs() template.FuncMap {
	return template.FuncMap{
		// join is here because ranging to build a class attribute is the one
		// thing every single template needs and the one thing templates are
		// worst at.
		"join": strings.Join,
		// hasPrefix answers "is this nav entry the current one" without the
		// host having to precompute it for every possible override.
		"hasPrefix": strings.HasPrefix,
	}
}
