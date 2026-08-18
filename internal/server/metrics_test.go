package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
)

// What an operator alerts on, and the one way it can quietly go wrong.

// THE BUG THIS EXISTS FOR. The obvious implementation reads r.Pattern in the
// middleware and gets an empty string every time — the mux sets that on the
// request it passes DOWN, and the middleware wraps the mux from outside. The
// symptom is every route labelled "other", which looks like a working metric.
//
// And the opposite failure is worse: labelling by the PATH gives one time
// series per workspace forever, which is how a metrics database runs out of
// memory, and publishes how many workspaces exist to whoever can scrape.
func TestTheRouteLabelIsTheTemplateAndNotThePath(t *testing.T) {
	d := newDoor(t)

	d.request(t, http.MethodGet, "/api/v1/workspaces/"+id(d.wsID)+"/schedules", d.adminTok, "")
	d.request(t, http.MethodGet, "/api/v1/workspaces", d.adminTok, "")

	out := d.scrape()
	if !strings.Contains(out, `route="/api/v1/workspaces/{id}/schedules"`) {
		t.Fatalf("the route label is not the template:\n%s", out)
	}
	// The id must not appear anywhere in a label.
	if strings.Contains(out, `route="/api/v1/workspaces/`+id(d.wsID)) {
		t.Fatalf("a workspace id reached a metric label:\n%s", out)
	}
	if strings.Contains(out, `route="other"`) {
		t.Fatalf("a matched route was bucketed as other:\n%s", out)
	}
}

// A scrape is a different audience from an authenticated screen. Nothing an
// operator named may appear in it.
func TestNoNameAPersonChoseReachesAMetric(t *testing.T) {
	d := newDoor(t)
	d.request(t, http.MethodGet, "/api/v1/workspaces", d.adminTok, "")

	out := d.scrape()
	// The fixture's workspace is called "operations" and its agent
	// "orchestrator". Neither is this scrape's business.
	for _, leak := range []string{"operations", "orchestrator", "admin"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q reached the metrics endpoint:\n%s", leak, out)
		}
	}
}

// The build is the one label everybody needs, and it is always there.
func TestTheBuildIsAlwaysPublished(t *testing.T) {
	d := newDoor(t)
	if out := d.scrape(); !strings.Contains(out, "cogitorium_build_info{version=") {
		t.Fatalf("no build info:\n%s", out)
	}
}

// Off is the default: this is a new thing listening on a machine, and a product
// that started one without being asked would be deciding that for somebody.
func TestTheMetricsListenerIsOffUnlessAskedFor(t *testing.T) {
	if d := config.Defaults(); d.MetricsListen != "" {
		t.Fatalf("metrics_listen defaults to %q; a scrape port must be asked for", d.MetricsListen)
	}
}

// scrape renders what the metrics endpoint would serve.
func (d *door) scrape() string {
	var b strings.Builder
	d.srv.metrics.Registry().Write(&b)
	return b.String()
}
