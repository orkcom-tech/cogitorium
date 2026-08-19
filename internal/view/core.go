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
}

// DefaultStrings is English, which is what this product ships in today. It is
// a value rather than constants so a plugin overriding the shell can be handed
// a different one without the host changing shape first.
func DefaultStrings() Strings {
	return Strings{Navigation: "Navigation", Instructions: "Instructions", Models: "Model catalog", Context: "Context", Gears: "Gears", Workspaces: "Workspaces", Variables: "Variables & Secrets"}
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
