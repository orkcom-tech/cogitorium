// Package plugin owns what a plugin declares about itself.
//
// One file, one format, one name: plugin.yaml sits at the bundle root and at
// the author's repository root, byte-identical, so the catalog reads exactly
// the file this server parses. A second copy is a second thing to drift.
//
// Everything a manifest can say is platform-free except the native tier. That
// is not a style preference — it is the parity guarantee written as a rule.
// A template is data and a WebAssembly module is data with an entry point, so
// a plugin that is only those is byte-identical on every target because it
// never had a platform to lose.
package plugin

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/orkcom-tech/cogitorium/internal/abi"
)

// Contract is the integer this build speaks. It is the real compatibility
// gate, and it moves only when the template model or the host ABI breaks —
// never for a feature addition, because a plugin that would still work must
// not be refused.
//
// Defined by the ABI rather than restated here. A manifest's declaration and
// the vocabulary its code speaks are the same promise, and two constants for
// one promise is one of them eventually being wrong.
const Contract = abi.Version

// SchemaVersion is the manifest's own shape, which is a different question
// from the contract. A manifest can gain optional fields without the host ABI
// moving at all.
const SchemaVersion = 1

// Manifest is plugin.yaml.
//
// Field order here is the order an author reads: who am I, what do I need from
// the host, what do I contribute to the interface, what do I need to run, and
// what am I asking permission for. That last group is deliberately last and
// deliberately separate — it is what the operator's approval screen is built
// from, and burying a grant among contributions would be a way to smuggle one.
type Manifest struct {
	Schema  int    `yaml:"schema"`
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	License string `yaml:"license"`
	Docs    string `yaml:"docs"`
	Source  string `yaml:"source"`

	Host Host `yaml:"host"`

	// ── interface: tier 0, needs no backend, works on every channel ──

	Pages   []Page   `yaml:"pages"`
	Nav     []Nav    `yaml:"nav"`
	Styles  []string `yaml:"styles"`
	Scripts []Script `yaml:"scripts"`

	// Overrides is ADVISORY and the system does not rely on it. What a plugin
	// actually overrides is computed from the templates it ships, because a
	// manifest can lie and parsed bytes cannot. Declaring accurately unlocks
	// nothing; it earns a quiet "manifest matches" line on the approval screen
	// and nothing more.
	Overrides []string `yaml:"overrides"`

	// ── behaviour ──

	// Needs is a technology and an optional constraint — "js", "python@>=3.11".
	// An author never writes a URL, an architecture, a platform, or the word
	// wasm. The host maps the technology to a tier and reports which it picked.
	Needs string `yaml:"needs"`

	// ── grants: what the operator is being asked to approve ──

	Hosts   []string `yaml:"hosts"`
	Secrets []string `yaml:"secrets"`
	API     []string `yaml:"api"`
}

// Host is what the plugin requires of this server.
type Host struct {
	// Contract is the integer gate. It is also checked against the backend
	// artifact's own exported ABI marker, so a manifest claiming a contract
	// its code does not speak is caught by the artifact rather than believed.
	Contract int `yaml:"contract"`
	// Cogitorium optionally narrows further — ">=1.9". An author uses this to
	// say "I depend on something added in 1.9" without the contract moving.
	Cogitorium string `yaml:"cogitorium"`
}

// Page is a route the host registers and renders from a named template.
//
// With no backend at all this already produces a working page: the host
// renders the template against a standard model. Adding a provider export
// later makes the same page dynamic without changing the route, the template
// name, or the URL — which is what keeps tier 0 a real destination rather
// than a waiting room.
type Page struct {
	Path     string `yaml:"path"`
	Template string `yaml:"template"`
	Title    string `yaml:"title"`
	// Auth defaults to "token". Writing "none" is allowed and is shown in red
	// on the approval screen beside the path, because every non-/api/ path in
	// this server is anonymous by construction and a plugin route is the first
	// time that default has been somebody else's to choose.
	Auth string `yaml:"auth"`
}

// Nav is the declarative way to add a destination.
//
// It exists because the template route to the same result is an append slot,
// and an author who reached for a plain override would define the rail's own
// name and silently erase every other plugin's entry. Four lines of YAML that
// compose is a better default than a correct template somebody has to know to
// write.
type Nav struct {
	Area  string `yaml:"area"`
	Label string `yaml:"label"`
	Icon  string `yaml:"icon"`
	Href  string `yaml:"href"`
	Order int    `yaml:"order"`
	When  string `yaml:"when"`
}

// Script is a head-injected module. Integrity is computed from the bundle at
// install, never declared here — a digest an author types is a digest an
// author can get wrong, and one that disagrees with the file is worse than
// none.
type Script struct {
	Src  string `yaml:"src"`
	Type string `yaml:"type"`
}

// Problem is one thing wrong with a manifest.
//
// Validation returns all of them rather than the first, because an author
// fixing a manifest wants the list, not a game where each run reveals one
// more. Field is the YAML path so an editor can be pointed at it.
type Problem struct {
	Field   string
	Message string
}

func (p Problem) Error() string { return p.Field + ": " + p.Message }

// Problems is the whole set, rendered as one message when treated as an error.
type Problems []Problem

func (ps Problems) Error() string {
	if len(ps) == 1 {
		return ps[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d problems in plugin.yaml:", len(ps))
	for _, p := range ps {
		b.WriteString("\n  " + p.Error())
	}
	return b.String()
}

// Parse reads plugin.yaml strictly.
//
// Unknown fields are an error rather than ignored. A typo'd key that parses
// silently is a contribution the author believes they made and the operator
// never sees, and the manifest is small enough that no field is optional by
// accident.
func Parse(b []byte) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("plugin.yaml could not be read: %w", err)
	}
	return m, nil
}

var (
	idRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	envRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	// Exactly what the gate accepts, and nothing more: a hostname, optionally
	// with the gate's own *. wildcard, and never a port. The gate refuses a
	// port outright — a grant names hosts — so accepting one here would let an
	// author write a grant that reads fine and fails when it is resolved.
	hostRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
)

// reserved ids would collide with the host's own namespace or with a path the
// server already owns. Refused at parse rather than at install, so an author
// learns before they have written anything against the name.
var reservedIDs = map[string]bool{
	"cog": true, "cogitorium": true, "core": true, "host": true,
	"plugin": true, "plugins": true, "api": true, "admin": true,
	"under": true, "static": true, "assets": true,
}

// Validate reports everything wrong with the manifest. An empty result means
// the manifest is well-formed — it does not mean the plugin works, which is
// what template validation and the tier resolver answer later.
func (m Manifest) Validate() Problems {
	var ps Problems
	add := func(field, msg string, a ...any) {
		ps = append(ps, Problem{Field: field, Message: fmt.Sprintf(msg, a...)})
	}

	if m.Schema != SchemaVersion {
		add("schema", "must be %d, got %d", SchemaVersion, m.Schema)
	}

	switch {
	case m.ID == "":
		add("id", "is required — it is the plugin's permanent name and its template namespace")
	case len(m.ID) < 3 || len(m.ID) > 48:
		add("id", "must be 3 to 48 characters, got %d", len(m.ID))
	case !idRe.MatchString(m.ID):
		add("id", "must be lowercase letters, digits and hyphens, and may not start or end with a hyphen")
	case reservedIDs[m.ID]:
		add("id", "%q is reserved by the host", m.ID)
	}

	if m.Name == "" {
		add("name", "is required — it is what an operator sees in the library")
	}
	if _, ok := parseVersion(m.Version); !ok {
		add("version", "must be semver like 1.4.0 or 1.4.0-beta.1, got %q", m.Version)
	}

	switch {
	case m.Host.Contract == 0:
		add("host.contract", "is required — it is the compatibility gate")
	case m.Host.Contract > Contract:
		add("host.contract", "asks for contract %d; this build speaks %d", m.Host.Contract, Contract)
	case m.Host.Contract < 1:
		add("host.contract", "must be a positive integer, got %d", m.Host.Contract)
	}
	if m.Host.Cogitorium != "" {
		if _, err := ParseConstraint(m.Host.Cogitorium); err != nil {
			add("host.cogitorium", "%v", err)
		}
	}

	// An OCI reference is a legitimate `needs` and does not follow the
	// technology grammar, so it is recognised before that grammar is applied.
	if m.Needs != "" && !looksLikeImage(m.Needs) {
		if _, err := ParseNeeds(m.Needs); err != nil {
			add("needs", "%v", err)
		}
	}

	m.validatePages(add)
	m.validateNav(add)
	m.validateAssets(add)
	m.validateOverrides(add)
	m.validateGrants(add)

	return ps
}

func (m Manifest) validatePages(add func(string, string, ...any)) {
	prefix := m.PagePrefix()
	seen := map[string]bool{}
	for i, p := range m.Pages {
		f := fmt.Sprintf("pages[%d]", i)
		switch {
		case p.Path == "":
			add(f+".path", "is required")
		case !strings.HasPrefix(p.Path, prefix):
			add(f+".path", "must be under %s so two plugins can never collide, got %q", prefix, p.Path)
		case p.Path != path.Clean(p.Path) && p.Path != path.Clean(p.Path)+"/":
			add(f+".path", "must be a clean path, got %q", p.Path)
		case seen[p.Path]:
			add(f+".path", "%q is declared twice", p.Path)
		default:
			seen[p.Path] = true
		}

		if p.Template == "" {
			add(f+".template", "is required")
		} else if err := m.checkTemplateName(p.Template); err != nil {
			add(f+".template", "%v", err)
		}

		switch p.Auth {
		case "", "token", "admin", "none":
		default:
			add(f+".auth", "must be token, admin or none, got %q", p.Auth)
		}
	}
}

func (m Manifest) validateNav(add func(string, string, ...any)) {
	for i, n := range m.Nav {
		f := fmt.Sprintf("nav[%d]", i)
		switch n.Area {
		case "", "rail":
		default:
			add(f+".area", "must be rail, got %q", n.Area)
		}
		if n.Label == "" {
			add(f+".label", "is required")
		}
		if n.Href == "" {
			add(f+".href", "is required")
		} else if !strings.HasPrefix(n.Href, "/") {
			add(f+".href", "must be an absolute path, got %q", n.Href)
		}
		switch n.When {
		case "", "always", "workspace", "admin":
		default:
			add(f+".when", "must be always, workspace or admin, got %q", n.When)
		}
	}
}

func (m Manifest) validateAssets(add func(string, string, ...any)) {
	check := func(field, p string) {
		switch {
		case p == "":
			add(field, "is required")
		case strings.HasPrefix(p, "/"):
			add(field, "must be relative to the bundle, got %q", p)
		case strings.Contains(p, ".."):
			add(field, "must not escape the bundle, got %q", p)
		}
	}
	for i, s := range m.Styles {
		check(fmt.Sprintf("styles[%d]", i), s)
	}
	for i, s := range m.Scripts {
		check(fmt.Sprintf("scripts[%d].src", i), s.Src)
		switch s.Type {
		case "", "module":
		default:
			add(fmt.Sprintf("scripts[%d].type", i), "must be module, got %q", s.Type)
		}
	}
}

func (m Manifest) validateOverrides(add func(string, string, ...any)) {
	for i, n := range m.Overrides {
		f := fmt.Sprintf("overrides[%d]", i)
		name, err := ParseName(n)
		if err != nil {
			add(f, "%v", err)
			continue
		}
		// Declaring a name you own is not an override, and an author who wrote
		// one has misunderstood the mechanism in a way worth naming now.
		if name.Namespace == m.ID {
			add(f, "%q is in your own namespace, so it adds rather than overrides — "+
				"remove it from overrides", n)
		}
	}
}

func (m Manifest) validateGrants(add func(string, string, ...any)) {
	for i, h := range m.Hosts {
		if !hostRe.MatchString(h) {
			add(fmt.Sprintf("hosts[%d]", i),
				"must be a hostname, optionally with a leading *. — and never a port, "+
					"because a grant names hosts. Got %q", h)
		}
	}
	for i, s := range m.Secrets {
		if !envRe.MatchString(s) {
			add(fmt.Sprintf("secrets[%d]", i),
				"must be an environment variable NAME, got %q — a manifest never carries a value", s)
		}
	}
	for i, s := range m.API {
		if !strings.Contains(s, ":") {
			add(fmt.Sprintf("api[%d]", i), "must be scope:action like runs:read, got %q", s)
		}
	}
}

// AuthDefault is what a page gets when its manifest says nothing. Closed, so
// forgetting the field is never the thing that opens a page.
const AuthDefault = "token"

// PagePrefix is the path space this plugin owns. Every page it registers lives
// under here, which is why no plugin can ever collide with another or with a
// route the server already serves.
func (m Manifest) PagePrefix() string { return "/p/" + m.ID + "/" }

// Namespace is the template prefix this plugin owns. Defining a name inside it
// adds; defining a name inside somebody else's overrides.
func (m Manifest) Namespace() string { return m.ID }

// checkTemplateName rejects a plugin's own page pointing at a name it does not
// own. Overriding somebody else's template is allowed and expected, but a page
// rendering a name the plugin did not ship is a dangling reference dressed up
// as a contribution.
func (m Manifest) checkTemplateName(s string) error {
	n, err := ParseName(s)
	if err != nil {
		return err
	}
	if n.Namespace != m.ID {
		return fmt.Errorf("a page must render a template you own; %q is in the %q namespace", s, n.Namespace)
	}
	return nil
}

// ── needs ─────────────────────────────────────────────────────────────────

// Needs is a parsed technology declaration. The tier it maps to is the host's
// decision and lives in the resolver, not here — this type only says what the
// author asked for.
type Need struct {
	Technology string
	Constraint Constraint
}

// ParseNeeds reads "python@>=3.11", or a bare "js" meaning any version.
func ParseNeeds(s string) (Need, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Need{}, nil
	}
	tech, rest, hasConstraint := strings.Cut(s, "@")
	tech = strings.TrimSpace(tech)
	if !idRe.MatchString(tech) {
		return Need{}, fmt.Errorf("technology must be lowercase letters, digits and hyphens, got %q", tech)
	}
	n := Need{Technology: tech}
	if hasConstraint {
		c, err := ParseConstraint(rest)
		if err != nil {
			return Need{}, err
		}
		n.Constraint = c
	}
	return n, nil
}

func (n Need) String() string {
	if n.Constraint.IsZero() {
		return n.Technology
	}
	return n.Technology + "@" + n.Constraint.String()
}

// ── versions and constraints ──────────────────────────────────────────────

// This is a deliberately separate parser from the one in internal/update, and
// the reason is that they answer different questions. That one decides whether
// a newer release exists and refuses to parse a prerelease on purpose, because
// telling somebody 1.6.0-rc1 is 1.6.0 would be wrong. A plugin author, though,
// legitimately ships 1.4.0-beta.1 and must be able to say so. Sharing one
// parser would mean one of the two questions gets the wrong answer.

// Version is a semver triple with an optional prerelease.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

func parseVersion(s string) (Version, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if s == "" {
		return Version{}, false
	}
	core, pre, _ := strings.Cut(s, "-")
	// Build metadata does not participate in comparison, so it is dropped
	// rather than stored — keeping it would invite somebody to compare it.
	core, _, _ = strings.Cut(core, "+")
	pre, _, _ = strings.Cut(pre, "+")

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var v Version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		switch i {
		case 0:
			v.Major = n
		case 1:
			v.Minor = n
		case 2:
			v.Patch = n
		}
	}
	v.Pre = pre
	return v, true
}

// ParseVersion is parseVersion with an error, for callers that report.
func ParseVersion(s string) (Version, error) {
	v, ok := parseVersion(s)
	if !ok {
		return Version{}, fmt.Errorf("%q is not a version like 1.4.0", s)
	}
	return v, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders two versions. A prerelease sorts before its release, which is
// the one rule that stops 1.4.0-beta.1 from satisfying ">=1.4.0".
func (v Version) Compare(o Version) int {
	switch {
	case v.Major != o.Major:
		return sign(v.Major - o.Major)
	case v.Minor != o.Minor:
		return sign(v.Minor - o.Minor)
	case v.Patch != o.Patch:
		return sign(v.Patch - o.Patch)
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return strings.Compare(v.Pre, o.Pre)
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	if n > 0 {
		return 1
	}
	return 0
}

// Constraint is a version requirement. Deliberately small: an operator reading
// an approval screen has to understand it, and a range grammar nobody can read
// aloud is a grammar that hides what a plugin actually demands.
type Constraint struct {
	Op string // "", ">=", ">", "=", "<", "<="
	V  Version
}

func (c Constraint) IsZero() bool { return c.Op == "" }

func (c Constraint) String() string {
	if c.IsZero() {
		return ""
	}
	return c.Op + c.V.String()
}

// ParseConstraint reads ">=1.9", "1.9.0", ">1.2.3", "<=2.0.0".
//
// A two-part version is accepted and completed with a zero patch, because
// ">=1.9" is what an author naturally writes and refusing it would be pedantry
// charged to the person we are trying to make comfortable.
func ParseConstraint(s string) (Constraint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Constraint{}, nil
	}
	op := "="
	for _, candidate := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(s, candidate) {
			op = candidate
			s = strings.TrimSpace(strings.TrimPrefix(s, candidate))
			break
		}
	}
	if parts := strings.Split(strings.TrimPrefix(s, "v"), "."); len(parts) == 2 {
		s += ".0"
	}
	v, err := ParseVersion(s)
	if err != nil {
		return Constraint{}, fmt.Errorf("%v — write it like \">=1.9\" or \"1.9.0\"", err)
	}
	return Constraint{Op: op, V: v}, nil
}

// Satisfied reports whether v meets the constraint. A zero constraint is
// satisfied by anything, which is what a bare "js" means.
func (c Constraint) Satisfied(v Version) bool {
	if c.IsZero() {
		return true
	}
	cmp := v.Compare(c.V)
	switch c.Op {
	case ">=":
		return cmp >= 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case "<":
		return cmp < 0
	default:
		return cmp == 0
	}
}
