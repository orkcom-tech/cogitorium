package view

import (
	"errors"
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
	// Compose is set when the plugin never got as far as being validated —
	// its templates would not parse, or it broke a naming rule.
	Compose error
}

// Reason renders the first failure as one line, which is what a row on the
// plugins page has space for.
func (d Disabled) Reason() string {
	if d.Compose != nil {
		return d.Compose.Error()
	}
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
			// A composition failure that belongs to a plugin drops that plugin
			// and the set is rebuilt without it. A plugin whose templates will
			// not parse is that plugin's problem — failing the whole boot for
			// it would let any stranger's typo take the product down.
			var le *LayerError
			if errors.As(err, &le) && le.Layer != plugin.CoreNamespace && !dropped[le.Layer] {
				dropped[le.Layer] = true
				report.Disabled = append(report.Disabled, Disabled{ID: le.Layer, Compose: le.Err})
				live = without(live, le.Layer)
				continue
			}
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

func without(sources []Source, id string) []Source {
	out := make([]Source, 0, len(sources))
	for _, s := range sources {
		if s.ID != id {
			out = append(out, s)
		}
	}
	return out
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
