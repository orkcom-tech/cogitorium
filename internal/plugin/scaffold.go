package plugin

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The first fifteen minutes.
//
// Everything here exists so an author's first plugin is a directory they can
// read, not a form they have to fill in from documentation. The default
// scaffold has no language, no compiler and no build step — because the
// cheapest tier is also the most common one, and making the simplest plugin
// genuinely simple is the whole promise.

// Scaffold writes a new plugin into dir.
//
// override, when given, seeds the bundle with a real template name so the
// author starts from something that renders rather than from a blank file and
// a naming rule to look up.
func Scaffold(dir, id, override string) error {
	if problems := scaffoldID(id); problems != "" {
		return fmt.Errorf("%s", problems)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty — a new plugin wants a directory of its own", dir)
	}
	if err := os.MkdirAll(filepath.Join(dir, templates), 0o755); err != nil {
		return err
	}

	files := map[string]string{
		manifestNm:                  scaffoldManifest(id, override),
		"README.md":                 scaffoldReadme(id, override),
		"theme.css":                 scaffoldTheme(),
		"templates/" + id + ".html": scaffoldPage(id),
	}
	if override != "" {
		files["templates/override.html"] = scaffoldOverride(override)
	}

	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func scaffoldID(id string) string {
	m := Manifest{Schema: SchemaVersion, ID: id, Name: id, Version: "0.1.0", Host: Host{Contract: Contract}}
	for _, p := range m.Validate() {
		if p.Field == "id" {
			return p.Message
		}
	}
	return ""
}

func scaffoldManifest(id, override string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# %s
#
# Everything here except native: is platform-free, which is why a plugin like
# this one runs the same on a laptop, in a container and in a cluster.
schema: 1
id: %s
name: %s
version: 0.1.0
license: Apache-2.0

host:
  # The compatibility gate. It moves only when the template model or the host
  # ABI breaks — never for an addition.
  contract: %d

# A page of your own. The host registers the route and renders the template;
# no backend is involved, which is why this plugin needs no build step.
pages:
  - path: /p/%s/
    template: %s.page.home
    title: %s
    # token (default) | admin | none. "none" is shown in red on the operator's
    # approval screen, because it is the one line that gives something away.
    auth: token

styles: [theme.css]
`, id, id, id, Contract, id, id, id)

	if override != "" {
		fmt.Fprintf(&b, `
# Declaring an override earns nothing and is not required — the host computes
# what you actually override from the templates you ship. It just means the
# operator's approval screen can say "matches what it declared".
overrides:
  - %s
`, override)
	}
	return b.String()
}

func scaffoldPage(id string) string {
	return fmt.Sprintf(`{{/*
  Your page. The model carries .Ctx (who is looking, where they are), .Params
  and .Query — and nothing else, because a page that needs more than that needs
  a backend, and pretending otherwise would hand you half a model.
*/}}
{{define "%s.page.home"}}
<main class="page">
  <h1>%s</h1>
  {{if .Ctx.Viewer.SignedIn}}
    <p>Hello, {{.Ctx.Viewer.Name}}.</p>
  {{end}}
</main>
{{end}}
`, id, id)
}

func scaffoldOverride(name string) string {
	return fmt.Sprintf(`{{/*
  An override of one of the product's own templates.

  under: is the body that was there before you — the host's, or another
  plugin's if one is layered below you. Calling it means you add rather than
  replace, and it means two plugins can wrap this same name without either
  having to know the other exists.

  Reaching past everybody to the product's own body is core:, and it has to be
  typed on purpose because it discards whatever the plugins below you did.
*/}}
{{define "%s"}}
  {{template "under:%s" .}}
{{end}}
`, name, name)
}

func scaffoldTheme() string {
	return `/* Overriding cog.shell.tokens restyles the whole product, in both light
   and dark, with no code at all. This file is the ordinary way in: it is
   injected after the product's own stylesheet. */
:root {
  /* --ground: #05070a; */
}
`
}

func scaffoldReadme(id, override string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nA Cogitorium plugin.\n\n## Trying it\n\n"+
		"```sh\ncogitorium plugins dev . --watch\n```\n\n"+
		"That registers this directory as a development layer and restarts the\n"+
		"server when a file changes. There is no build step: a template is data\n"+
		"and the renderer is already inside the binary.\n\n"+
		"## Publishing it\n\n```sh\ncogitorium plugins build .\n```\n\n"+
		"produces a bundle somebody else can install.\n", id)
	if override != "" {
		fmt.Fprintf(&b, "\n## What it overrides\n\nThis plugin takes over `%s`. "+
			"It calls `under:` so whatever was there still renders — remove that line "+
			"to replace it outright.\n", override)
	}
	return b.String()
}

// ── building a bundle ─────────────────────────────────────────────────────

// skipped names never belong in a bundle. Version-control metadata and
// editor droppings are the common ones, and shipping them means an author's
// local paths and history travel to somebody else's machine.
var skipped = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".DS_Store": true,
	"node_modules": true, ".idea": true, ".vscode": true,
}

// Build packages a plugin directory into a bundle beside it.
//
// The manifest is validated first, so an author learns their plugin is
// unloadable here rather than after somebody else has downloaded it.
func Build(dir, outDir string) (string, Manifest, error) {
	mb, err := os.ReadFile(filepath.Join(dir, manifestNm))
	if err != nil {
		return "", Manifest{}, fmt.Errorf("%s: %w", dir, err)
	}
	m, err := Parse(mb)
	if err != nil {
		return "", Manifest{}, err
	}
	if ps := m.Validate(); len(ps) > 0 {
		return "", Manifest{}, ps
	}
	if err := checkBundleReferences(dir, m); err != nil {
		return "", Manifest{}, err
	}

	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", Manifest{}, err
	}
	out := filepath.Join(outDir, fmt.Sprintf("%s-%s.zip", m.ID, m.Version))

	// Written to a temporary file and renamed, because the default output
	// directory IS the directory being walked — building in place would
	// otherwise include the archive in itself, half-written, and produce a zip
	// that reads as corrupt with no clue why. Also means a failed build leaves
	// the previous bundle rather than a truncated one.
	f, err := os.CreateTemp(outDir, ".cogitorium-build-*.zip")
	if err != nil {
		return "", Manifest{}, err
	}
	tmpName := f.Name()
	defer func() {
		f.Close()
		os.Remove(tmpName)
	}()

	absOut, _ := filepath.Abs(out)
	zw := zip.NewWriter(f)

	// A ceiling, as well as the exclusions above.
	//
	// The exclusions are the fix; this is the belt. Writing an archive into
	// the directory being walked is a mistake that does not fail — it
	// recurses, and the first symptom is a disk filling up. It cost 28GB
	// before it was noticed, and a plugin bundle that is approaching this is
	// wrong whatever the reason.
	var written int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skipped[d.Name()] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// The archive being written, and any bundle left from a previous
		// build, are not part of the plugin.
		if abs, err := filepath.Abs(p); err == nil && (abs == tmpName || abs == absOut) {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".cogitorium-build-") {
			return nil
		}
		// A bundle holds files and directories only. A symlink is a way out of
		// the tree on the other machine, and the unpacker refuses one anyway —
		// refusing it here means the author hears about it.
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file; a bundle holds files and directories only", rel)
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()

		n, err := io.Copy(w, io.LimitReader(src, maxBundleBytes-written+1))
		written += n
		if err != nil {
			return err
		}
		if written > maxBundleBytes {
			return fmt.Errorf("this bundle passed %d MB while packing %s, which is not a plugin "+
				"— check for an archive or a build directory inside it",
				int64(maxBundleBytes)>>20, rel)
		}
		return nil
	})
	if err != nil {
		zw.Close()
		return "", Manifest{}, err
	}
	if err := zw.Close(); err != nil {
		return "", Manifest{}, err
	}
	if err := f.Close(); err != nil {
		return "", Manifest{}, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", Manifest{}, err
	}
	if err := os.Rename(tmpName, out); err != nil {
		return "", Manifest{}, err
	}
	return out, m, nil
}

// checkBundleReferences catches a manifest naming a file that is not there.
//
// At build time, where the author can fix it — rather than at install time on
// somebody else's machine, where it is a stylesheet that quietly never
// arrives.
func checkBundleReferences(dir string, m Manifest) error {
	var missing []string
	check := func(rel string) {
		if rel == "" {
			return
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			missing = append(missing, rel)
		}
	}
	for _, s := range m.Styles {
		check(s)
	}
	for _, s := range m.Scripts {
		check(s.Src)
	}
	for _, n := range m.Native {
		check(n.Path)
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("the manifest names %d file(s) this directory does not have: %s",
		len(missing), strings.Join(missing, ", "))
}
