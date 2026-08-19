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
}

// DefaultStrings is English, which is what this product ships in today. It is
// a value rather than constants so a plugin overriding the shell can be handed
// a different one without the host changing shape first.
func DefaultStrings() Strings {
	return Strings{Navigation: "Navigation"}
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
	}
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
