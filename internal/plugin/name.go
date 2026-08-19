package plugin

import (
	"fmt"
	"regexp"
	"strings"
)

// A template name is an address, not a description of markup.
//
// Ownership is encoded in the first segment, and that single decision is what
// makes the whole override system work without a manifest: core owns "cog.",
// a plugin owns its own id, and defining a name inside somebody else's prefix
// IS the override. Detection is a prefix test and a set difference over
// parsed bytes, so a manifest that lies about what it overrides changes
// nothing — which is exactly what "declaration is advisory" has to mean if it
// is going to be true.
//
// The rules exist so an override survives the host redesigning its own markup:
//
//   - a name addresses a subject, never an element, a route or a CSS class
//   - every repeated unit has its own name, so a list container can be rebuilt
//     without touching the row overrides hanging off it
//   - every plausible extension region has a name even where the host's body
//     is empty
//   - names never carry a version
//   - a shipped name is never reused for a different subject

// Area is the closed vocabulary a name's second segment is drawn from. Closed
// on purpose: an open set would drift into a second naming scheme within a
// year, and the words here are the ones the product already says out loud.
type Area string

const (
	// AreaShell is chrome that surrounds everything — the document, the rail,
	// the frame.
	AreaShell Area = "shell"
	// AreaPage is a whole destination.
	AreaPage Area = "page"
	// AreaStage is a major region within a page.
	AreaStage Area = "stage"
	// AreaDrawer is a panel that opens over a stage.
	AreaDrawer Area = "drawer"
	// AreaList is a container of repeated units.
	AreaList Area = "list"
	// AreaRow is one repeated unit. Named separately from its list so the
	// container can be rebuilt without breaking every row override.
	AreaRow Area = "row"
	// AreaField is one labelled value.
	AreaField Area = "field"
	// AreaAction is one control. Actions are data in the model as well —
	// this is for rendering one, not for declaring one.
	AreaAction Area = "action"
	// AreaEmpty is what a container shows when it holds nothing.
	AreaEmpty Area = "empty"
	// AreaBadge is a small status mark.
	AreaBadge Area = "badge"
	// AreaFrag is a streamed fragment. It binds to the same model as the
	// region it replaces, so in the common case a fragment override and a
	// full-region override are literally the same body.
	AreaFrag Area = "frag"
	// AreaSlot is an extension point that CONCATENATES rather than replaces.
	// See Name.Appends.
	AreaSlot Area = "slot"
)

var areas = map[Area]bool{
	AreaShell: true, AreaPage: true, AreaStage: true, AreaDrawer: true,
	AreaList: true, AreaRow: true, AreaField: true, AreaAction: true,
	AreaEmpty: true, AreaBadge: true, AreaFrag: true, AreaSlot: true,
}

// Areas lists the vocabulary, for an error message that tells an author what
// they may have meant instead of only what they got wrong.
func Areas() []string {
	return []string{
		string(AreaShell), string(AreaPage), string(AreaStage), string(AreaDrawer),
		string(AreaList), string(AreaRow), string(AreaField), string(AreaAction),
		string(AreaEmpty), string(AreaBadge), string(AreaFrag), string(AreaSlot),
	}
}

// CoreNamespace is the host's own prefix. A plugin defining a name here is
// overriding the product itself, which is allowed and is the point.
const CoreNamespace = "cog"

// Name is a parsed template name.
type Name struct {
	Namespace string
	Area      Area
	// Subject and the optional parts after it, joined by dots as written.
	Subject string
	Full    string
}

var segmentRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// versionish catches a subject segment that is trying to be a version. Names
// never carry one: a name is an address, and an address that changes when the
// thing at it changes is not an address.
//
// Checked only on the segments AFTER the area, never on the namespace, because
// a plugin is entitled to an id like "2048" and refusing its every template
// would be this rule firing on the one case it was not written for.
var versionish = regexp.MustCompile(`^v?[0-9]+([._-][0-9]+)*$`)

// ParseName reads "<namespace>.<area>.<subject>[.<part>...]".
func ParseName(s string) (Name, error) {
	if s == "" {
		return Name{}, fmt.Errorf("a template name is required")
	}
	if s != strings.ToLower(s) {
		return Name{}, fmt.Errorf("%q must be lowercase", s)
	}
	parts := strings.Split(s, ".")
	if len(parts) < 3 {
		return Name{}, fmt.Errorf("%q must be <namespace>.<area>.<subject>, for example cog.row.gear", s)
	}
	for i, p := range parts {
		if !segmentRe.MatchString(p) {
			return Name{}, fmt.Errorf("%q has an invalid segment %q — segments are lowercase letters, "+
				"digits and hyphens, and may not start or end with a hyphen", s, p)
		}
		if i >= 2 && versionish.MatchString(p) {
			return Name{}, fmt.Errorf("%q looks like it carries a version in segment %q; "+
				"a name is an address and never carries one", s, p)
		}
	}
	area := Area(parts[1])
	if !areas[area] {
		return Name{}, fmt.Errorf("%q uses unknown area %q — it must be one of: %s",
			s, parts[1], strings.Join(Areas(), ", "))
	}
	return Name{
		Namespace: parts[0],
		Area:      area,
		Subject:   strings.Join(parts[2:], "."),
		Full:      s,
	}, nil
}

// MustParseName is for compiled-in host names, where a bad name is a bug in
// this repository rather than in somebody's plugin.
func MustParseName(s string) Name {
	n, err := ParseName(s)
	if err != nil {
		panic("plugin: bad host template name: " + err.Error())
	}
	return n
}

// IsCore reports whether the name belongs to the host.
func (n Name) IsCore() bool { return n.Namespace == CoreNamespace }

// OwnedBy reports whether the plugin with this id owns the name — that is,
// whether defining it adds rather than overrides.
func (n Name) OwnedBy(id string) bool { return n.Namespace == id }

// Appends reports whether definitions of this name CONCATENATE instead of the
// last one winning.
//
// This is the correction that matters most in the whole system. If every name
// were last-wins, then two plugins each adding an entry to the rail would both
// define the rail's own name and only the later one's entry would survive —
// two strangers from the catalog silently erasing each other, with neither
// able to prevent it from their side. Addition and replacement are different
// operations, so they get different names.
func (n Name) Appends() bool {
	return n.Area == AreaSlot || strings.HasSuffix(n.Full, ".extra")
}

func (n Name) String() string { return n.Full }

// ── aliases ───────────────────────────────────────────────────────────────

// An override that wants to add rather than replace calls what it replaced.
// Two aliases exist and the default is the one authors should almost always
// use.
const (
	// AliasUnder resolves to the definition immediately below this layer —
	// which is the previous plugin's wrapper if there is one, and the host's
	// body if there is not. This is the default and what the bare alias means.
	// It is why two plugins can both wrap the same row and both survive
	// without ever knowing about each other.
	AliasUnder = "under:"
	// AliasCore resolves past every plugin to the host's own body. It has to
	// be typed on purpose, because reaching for it discards whatever the
	// plugins below did.
	AliasCore = "core:"
)

// SplitAlias reports the alias prefix on a template reference and the name
// beneath it. A reference with no prefix is not an alias at all.
func SplitAlias(ref string) (alias, name string, ok bool) {
	switch {
	case strings.HasPrefix(ref, AliasUnder):
		return AliasUnder, strings.TrimPrefix(ref, AliasUnder), true
	case strings.HasPrefix(ref, AliasCore):
		return AliasCore, strings.TrimPrefix(ref, AliasCore), true
	}
	return "", ref, false
}

// Dormant names a template that is registered and validated but that nothing
// currently calls, so overriding it changes nothing on screen.
//
// The rail is the whole of it today: the vocabulary was fixed before the rail
// itself was converted, so the names are stable for authors to write against
// while the thing that would render them is still the application's.
//
// This exists so tooling can say so out loud. An author who overrides a
// dormant name gets a plugin that installs, validates, loads and does nothing
// — the single most expensive way to learn how this system works, and one
// that reads as "my plugin is broken" rather than "this is not wired yet".
// Every entry here is a line item to delete as its screen converts.
func Dormant(name string) bool {
	switch name {
	case "cog.shell.rail", "cog.row.nav", "cog.slot.rail":
		return true
	}
	return false
}

