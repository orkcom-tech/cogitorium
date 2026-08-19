package plugin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/channel"
)

// An author declares a technology. The host decides the tier. The tier the
// host picks is never the author's problem.
//
// That sentence is the whole design. An author writes `needs: python@>=3.11`
// and never writes a URL, a version, an architecture, a platform, or the word
// wasm. This file is where the declaration becomes a decision, and the
// decision is made against what this install can actually do — before anything
// is fetched, so a plugin that cannot run here is refused with a sentence
// instead of downloaded and then found wanting.

// Tier is how a plugin's backend runs, if it has one.
type Tier string

const (
	// TierBundle is no backend at all: templates, styles, islands, assets.
	// Works on every channel unconditionally, because a template is data and
	// the renderer is compiled into the binary the operator already installed.
	TierBundle Tier = "bundle"
	// TierWasm is the universal runtime — a WebAssembly module executed by the
	// engine inside this binary. Also unconditional. JavaScript lands here
	// through an embedded provider, so a JS plugin runs on the Alpine image
	// with no Node anywhere near it.
	TierWasm Tier = "wasm"
	// TierProvisioned is an interpreter fetched into the data directory and
	// shared by every plugin that needs that version. Available on every
	// channel by default, but guaranteed by probe rather than by construction:
	// it needs to be able to execute a file it wrote.
	TierProvisioned Tier = "provisioned"
	// TierImage is an OCI image run on the sandbox this server already has.
	// Bound to the live sandbox backend, never to the channel's name.
	TierImage Tier = "image"
	// TierNative is per-target binaries the author published. The only
	// platform-keyed structure in the whole system, reached only by typing
	// "native" on purpose.
	TierNative Tier = "native"
)

// technology is one entry in the compiled-in table.
type technology struct {
	tier Tier
	// supersededBy is the ratchet. A technology that gets renamed keeps
	// working: the old name resolves to the new one and the plugin is told,
	// rather than every published plugin naming it becoming uninstallable.
	supersededBy string
	// note is added to the resolution so an author or operator learns why they
	// got the tier they got.
	note string
}

// technologies is the whole vocabulary. Closed on purpose: an author asking
// for something not in here gets a refusal listing what exists, which is a
// better answer than a fetch that fails later against a name nobody owns.
var technologies = map[string]technology{
	// The universal tier.
	"js":         {tier: TierWasm, note: "runs on the JavaScript engine inside this binary"},
	"javascript": {tier: TierWasm, supersededBy: "js"},
	"wasm":       {tier: TierWasm},
	"rust":       {tier: TierWasm},
	"tinygo":     {tier: TierWasm},
	"go":         {tier: TierWasm},
	"zig":        {tier: TierWasm},
	"c":          {tier: TierWasm},

	// Fetched interpreters.
	"python": {tier: TierProvisioned},
	"node":   {tier: TierProvisioned},
	"bun":    {tier: TierProvisioned},
	"deno":   {tier: TierProvisioned},

	// Opt-in.
	"native": {tier: TierNative},
}

// Technologies lists the vocabulary for an error that tells an author what
// they could have written.
func Technologies() []string {
	out := make([]string, 0, len(technologies))
	for name, t := range technologies {
		if t.supersededBy == "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Capabilities is what this install can do beyond what the channel probe
// answers on its own.
type Capabilities struct {
	Profile channel.Profile
	// ContainerRunner reports whether the LIVE sandbox backend can run a
	// container. Deliberately a fact about the backend rather than about the
	// channel name: the shipped compose image is a container itself and still
	// cannot run one, while a native install with Docker can.
	ContainerRunner bool
}

// Resolution is the answer: which tier, whether it can run here, and if not,
// a sentence naming the plugin, the runtime and the channel.
type Resolution struct {
	Tier       Tier
	Technology string
	// Superseded is set when the author's spelling was an older name that
	// still works. The plugin is not refused for it.
	Superseded string
	Available  bool
	// Native is the row that matched, for the native tier only.
	Native Native
	// Refusal is empty when Available. It is written to be read by a person
	// deciding what to do next, so it names what is wrong and what still works.
	Refusal string
	Note    string
}

// Resolve decides how a plugin's backend would run here, fetching nothing.
//
// Nothing is downloaded before this answers, which is the point: an operator
// on a hardened cluster learns that a plugin needs an interpreter their data
// volume will not execute on the approval screen, not after a 27 MB download.
func Resolve(m Manifest, c Capabilities) Resolution {
	if strings.TrimSpace(m.Needs) == "" {
		return Resolution{Tier: TierBundle, Available: true,
			Note: "no backend — templates and assets only, so it runs on every channel"}
	}

	// An image reference is recognised by its shape rather than by a keyword,
	// because that is how an author naturally writes one — and it has to be
	// recognised BEFORE the technology grammar, or a registry path fails the
	// name rules on its way to being understood.
	if looksLikeImage(m.Needs) {
		return resolveImage(m, c)
	}

	need, err := ParseNeeds(m.Needs)
	if err != nil {
		return Resolution{Refusal: fmt.Sprintf("plugin %q declares an unreadable `needs`: %v", m.ID, err)}
	}

	t, known := technologies[need.Technology]
	if !known {
		return Resolution{
			Technology: need.Technology,
			Refusal: fmt.Sprintf("plugin %q needs %q, which this build does not know. "+
				"Available: %s — or name an OCI image to run it in a container.",
				m.ID, need.Technology, strings.Join(Technologies(), ", ")),
		}
	}

	r := Resolution{Technology: need.Technology, Tier: t.tier, Note: t.note}
	if t.supersededBy != "" {
		r.Superseded = t.supersededBy
		r.Technology = t.supersededBy
		r.Tier = technologies[t.supersededBy].tier
		r.Note = fmt.Sprintf("%q is the older name for %q and still works", need.Technology, t.supersededBy)
	}

	switch r.Tier {
	case TierWasm:
		// Guaranteed by construction. The engine is inside this binary, the
		// module has no platform, and where the compiler cannot run the
		// interpreter runs the identical module more slowly. Degradation is
		// speed, never compatibility.
		r.Available = true
	case TierProvisioned:
		resolveProvisioned(&r, m, c)
	case TierNative:
		resolveNative(&r, m, c)
	}
	return r
}

func resolveProvisioned(r *Resolution, m Manifest, c Capabilities) {
	p := c.Profile

	// Deno publishes no musl artifact at all, so on the Alpine image there is
	// nothing to fetch. Said as a fact with the alternative named, because an
	// author hearing "no" wants to know what to write instead.
	if r.Technology == "deno" && p.Libc == channel.Musl {
		r.Refusal = fmt.Sprintf("plugin %q needs Deno, which publishes no musl build, and this install "+
			"runs on musl (%s). Bun covers the same ground here — or use the JavaScript "+
			"tier, which needs nothing fetched at all.", m.ID, p.Kind)
		return
	}

	if !p.CanExecFromData {
		r.Refusal = fmt.Sprintf("plugin %q needs a %s runtime fetched into the data directory, and "+
			"this install cannot execute one: %s", m.ID, r.Technology, p.ExecRefusal)
		return
	}
	r.Available = true
	r.Note = fmt.Sprintf("one %s runtime is fetched per version and shared by every plugin that needs it",
		r.Technology)
}

func resolveNative(r *Resolution, m Manifest, c Capabilities) {
	p := c.Profile
	libc := string(p.Libc)
	if libc == "" {
		libc = "any"
	}
	want := fmt.Sprintf("%s/%s/%s", p.OS, p.Arch, libc)
	anyLibc := fmt.Sprintf("%s/%s/any", p.OS, p.Arch)

	var have []string
	for _, n := range m.Native {
		have = append(have, n.Target())
		if n.Target() == want || n.Target() == anyLibc {
			r.Available = true
			r.Native = n
			r.Note = "runs the author's published binary for " + n.Target()
			return
		}
	}
	published := "nothing"
	if len(have) > 0 {
		published = strings.Join(have, ", ")
	}
	r.Refusal = fmt.Sprintf("plugin %q is a native plugin and published %s, but this install is %s. "+
		"Ask its author for that target.", m.ID, published, want)
}

func resolveImage(m Manifest, c Capabilities) Resolution {
	r := Resolution{Tier: TierImage, Technology: m.Needs}
	if !c.ContainerRunner {
		r.Refusal = fmt.Sprintf("plugin %q runs in a container image, and this install has no sandbox "+
			"backend that can start one (channel %s). A native install with Docker, or a "+
			"Kubernetes deployment, can run it.", m.ID, c.Profile.Kind)
		return r
	}
	r.Available = true
	r.Note = "runs one container per invocation on the sandbox this server already has"
	return r
}

// looksLikeImage recognises an OCI reference by shape. A registry host has a
// dot or a port before the first slash, and a digest or tag follows the name —
// none of which a technology keyword ever has.
func looksLikeImage(s string) bool {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		return true
	}
	// "python@>=3.11" is a constraint; "python@sha256:..." is a digest.
	if _, rest, ok := strings.Cut(s, "@"); ok {
		return strings.HasPrefix(rest, "sha256:")
	}
	return false
}

// Universal reports whether a tier runs everywhere with nothing fetched and
// nothing probed. It is what the library screen uses to say "works everywhere"
// without qualification.
func (t Tier) Universal() bool { return t == TierBundle || t == TierWasm }

func (t Tier) String() string { return string(t) }
