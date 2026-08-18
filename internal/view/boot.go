package view

import (
	"fmt"
	"html/template"
	"io/fs"
	"sort"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
)

// Boot is where "fail the plugin, not the boot" stops being a slogan.
//
// Composing and validating are separate steps, and a plugin can only be found
// broken after it has been composed in. So the set is built, validated, and —
// if anything failed — rebuilt without the plugins that failed. It repeats,
// because dropping a plugin changes what is beneath the ones above it: a
// wrapper whose body came from the plugin just removed now wraps something
// else, and that has to be validated too.
//
// The loop terminates because each pass drops at least one plugin and never
// adds one back.

// Source is one plugin's templates plus the identity to blame for them.
type Source struct {
	ID string
	FS fs.FS
}

// Disabled is one plugin that did not survive, and why. The reason is written
// for the plugins page, so it names the plugin, the template and the field
// rather than quoting a Go error at somebody.
type Disabled struct {
	ID       string
	Failures []Failure
}

// Reason renders the first failure as one line, which is what a row on the
// plugins page has space for.
func (d Disabled) Reason() string {
	if len(d.Failures) == 0 {
		return "disabled"
	}
	if n := len(d.Failures); n > 1 {
		return fmt.Sprintf("%s (and %d more)", d.Failures[0].String(), n-1)
	}
	return d.Failures[0].String()
}

// BootReport is what happened, for the log line and the plugins page.
type BootReport struct {
	// Loaded is the plugins whose templates are live, in layer order.
	Loaded []string
	// Disabled is the plugins that were dropped, each with its reason.
	Disabled []Disabled
	// Warnings are things inert rather than broken — they render, they just do
	// not do what their author probably meant.
	Warnings []Warning
	// Unvalidated names had no registered model. Reported so a check that
	// covers less than it appears to cannot be mistaken for one that covers
	// everything.
	Unvalidated []string
}

// OK reports whether every plugin survived.
func (r BootReport) OK() bool { return len(r.Disabled) == 0 }

// Boot composes the host's templates with every enabled plugin, drops the ones
// that cannot render, and returns the set that is actually safe to serve.
//
// A failure in the host's own layer is fatal and returns an error. That is the
// one loud exception: if the product's own templates cannot render there is
// nothing to serve, and starting anyway would put a broken page in front of
// somebody instead of a clear refusal in a log.
func Boot(funcs template.FuncMap, core fs.FS, plugins []Source, models Models) (*Set, BootReport, error) {
	live := make([]Source, len(plugins))
	copy(live, plugins)

	var report BootReport
	dropped := map[string]bool{}

	for {
		layers := make([]Layer, 0, len(live)+1)
		layers = append(layers, Layer{ID: plugin.CoreNamespace, FS: core})
		for _, p := range live {
			layers = append(layers, Layer{ID: p.ID, FS: p.FS})
		}

		set, err := Compose(funcs, layers...)
		if err != nil {
			// Composition failed rather than validation, so nothing has been
			// attributed yet. If a plugin's own templates are unparseable the
			// error names its layer; there is no way to continue without
			// knowing which, so this is reported as-is.
			return nil, report, err
		}

		vr := Validate(set, models)
		failed := vr.FailedLayers()

		if hostFailures, bad := failed[plugin.CoreNamespace]; bad {
			return nil, report, fmt.Errorf(
				"the product's own templates cannot render, so there is nothing to serve: %v",
				hostFailures[0])
		}

		if len(failed) == 0 {
			report.Warnings = set.Ledger().Warnings
			report.Unvalidated = vr.Unvalidated
			for _, p := range live {
				report.Loaded = append(report.Loaded, p.ID)
			}
			sort.Slice(report.Disabled, func(i, j int) bool {
				return report.Disabled[i].ID < report.Disabled[j].ID
			})
			return set, report, nil
		}

		// Drop everything that failed this pass and try again. Dropping one at
		// a time would be tidier to reason about and would also mean N passes
		// for N broken plugins, each parsing the whole set.
		var kept []Source
		for _, p := range live {
			if fails, bad := failed[p.ID]; bad && !dropped[p.ID] {
				dropped[p.ID] = true
				report.Disabled = append(report.Disabled, Disabled{ID: p.ID, Failures: fails})
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == len(live) {
			// Nothing was dropped but something failed, which would loop
			// forever. It can only happen if a failure was attributed to a
			// layer that is not in the list, and that is a bug here rather
			// than in somebody's plugin.
			return nil, report, fmt.Errorf(
				"view: a template failed but no plugin owns it; this is a bug in the host")
		}
		live = kept
	}
}

// Sources builds the layer inputs from what the store says is enabled. It is a
// small adapter kept here rather than in the store, so the store never has to
// know what a template is.
func Sources(installed []plugin.Installed) ([]Source, error) {
	out := make([]Source, 0, len(installed))
	for _, in := range installed {
		fsys, err := in.Templates()
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", in.Manifest.ID, err)
		}
		out = append(out, Source{ID: in.Manifest.ID, FS: fsys})
	}
	return out, nil
}
