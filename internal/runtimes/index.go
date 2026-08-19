// Package runtimes fetches the interpreter a provisioned plugin needs.
//
// This is the tier that is available on every channel by DEFAULT but
// guaranteed by probe rather than by construction. A template and a wasm module
// need nothing; an interpreter needs somewhere it can be written and then
// executed, and a hardened data volume denies exactly that. The difference is
// reported before an operator approves an install, not discovered afterwards.
//
// Resolution reads a pinned table compiled into this binary. It is never a
// live query against a release API, and that is the load-bearing decision here:
// an install with no network fails naming the row it wanted, rather than
// quietly reaching for whatever "latest" means today. What a plugin runs on is
// a property of the Cogitorium version an operator installed, and it changes
// when they upgrade it — deliberately, with the digests in a diff somebody can
// read.
package runtimes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/channel"
)

//go:embed index.json
var indexJSON []byte

// Row is one fetchable runtime, for one technology on one target.
type Row struct {
	Technology string `json:"technology"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	// Libc is empty where the question does not arise — macOS and Windows.
	// It is part of the key because a glibc build does not start on the Alpine
	// image and the failure says nothing about the cause.
	Libc string `json:"libc"`

	URL    string `json:"url"`
	SHA256 string `json:"sha256"`

	// Root is the directory the archive unpacks into, which upstream chooses
	// and which the host must not guess.
	Root string `json:"root"`
	// Exe is the interpreter inside the unpacked tree, relative to the version
	// directory.
	Exe string `json:"exe"`
}

// Target is the tuple a row is keyed on.
func (r Row) Target() string {
	if r.Libc == "" {
		return r.OS + "/" + r.Arch
	}
	return r.OS + "/" + r.Arch + "/" + r.Libc
}

// Index is the pinned table.
type Index struct {
	Note string `json:"note"`
	Rows []Row  `json:"rows"`
}

var index Index

func init() {
	if err := json.Unmarshal(indexJSON, &index); err != nil {
		// Compiled in, so this is a bug in this repository rather than
		// anything an operator can cause.
		panic("runtimes: the pinned index does not parse: " + err.Error())
	}
}

// All returns every pinned row.
func All() []Row { return append([]Row(nil), index.Rows...) }

// Technologies lists what can be provisioned at all, for a refusal that tells
// somebody what they could have asked for.
func Technologies() []string {
	seen := map[string]bool{}
	for _, r := range index.Rows {
		seen[r.Technology] = true
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Versions lists the versions pinned for one technology, newest last.
func Versions(tech string) []string {
	seen := map[string]bool{}
	for _, r := range index.Rows {
		if r.Technology == tech {
			seen[r.Version] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Satisfies is the version test a caller supplies, so this package does not
// need to know how constraints are written.
type Satisfies func(version string) bool

// Select finds the row for a technology on this machine.
//
// The refusal names the technology, the target and what IS pinned, because the
// person reading it is deciding whether to change the plugin, change the
// install, or file a request for a target nobody built — and those are three
// different actions from three different facts.
func Select(tech string, ok Satisfies, p channel.Profile) (Row, error) {
	target := targetOf(p)

	var forTech, forTarget []Row
	for _, r := range index.Rows {
		if r.Technology != tech {
			continue
		}
		forTech = append(forTech, r)
		if r.Target() != target {
			continue
		}
		forTarget = append(forTarget, r)
	}

	if len(forTech) == 0 {
		return Row{}, fmt.Errorf("this build pins no %s runtime. It pins: %s",
			tech, strings.Join(Technologies(), ", "))
	}
	if len(forTarget) == 0 {
		return Row{}, fmt.Errorf("this build pins %s but not for %s. It has: %s",
			tech, target, strings.Join(targetsOf(forTech), ", "))
	}

	// Newest satisfying version wins. Sorted by string because the pinned
	// versions of one technology share a shape, and a full comparison here
	// would be a second version parser in a codebase that already has two for
	// two different questions.
	sort.Slice(forTarget, func(i, j int) bool { return forTarget[i].Version > forTarget[j].Version })
	for _, r := range forTarget {
		if ok == nil || ok(r.Version) {
			return r, nil
		}
	}
	return Row{}, fmt.Errorf("this build pins %s for %s at %s, and none of those satisfy what the "+
		"plugin asked for. Upgrading Cogitorium is what moves this list",
		tech, target, strings.Join(Versions(tech), ", "))
}

func targetOf(p channel.Profile) string {
	if p.Libc == channel.LibcNone {
		return p.OS + "/" + p.Arch
	}
	return p.OS + "/" + p.Arch + "/" + string(p.Libc)
}

func targetsOf(rows []Row) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Target()] = true
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
