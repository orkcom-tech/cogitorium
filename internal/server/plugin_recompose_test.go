package server

import (
	"archive/zip"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
)

// Removing a plugin has to take what it added off the screen.
//
// It did not. The interface was composed once, at boot, so everything a plugin
// contributed — its entry in the menu, its overrides, its pages — stayed
// exactly where it was until somebody restarted the install. Removing one and
// reloading the page showed it still there, which reads as the removal having
// silently failed.
func TestRemovingAPluginTakesItOffTheScreen(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	store, err := plugin.Open(in.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Install(navPlugin(t, "radar")); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := store.Approve("radar", "admin"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Enabled through the handler, which is what an operator presses.
	rec := in.request(t, "POST", "/api/v1/plugins/radar/enable", in.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"restart_required":false`) {
		t.Errorf("enabling a plugin with no backend asked for a restart it does not need: %s", rec.Body.String())
	}
	if !inTheRail(in, "radar") {
		t.Fatal("the plugin was enabled and its entry never appeared: recomposing did not happen")
	}

	rec = in.request(t, "DELETE", "/api/v1/plugins/radar", in.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"restart_required":false`) {
		t.Errorf("removing a plugin with no backend asked for a restart it does not need: %s", rec.Body.String())
	}
	if inTheRail(in, "radar") {
		t.Fatal("the plugin was removed and its entry is still in the menu — which is what somebody " +
			"sees when they delete a plugin, reload, and find it still there")
	}
}

// Switching a plugin off is the same promise as removing it: what it added is
// gone from the next page, not from the next restart.
func TestDisablingAPluginTakesItOffTheScreen(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	store, err := plugin.Open(in.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Install(navPlugin(t, "radar")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve("radar", "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := in.request(t, "POST", "/api/v1/plugins/radar/enable", in.adminTok, ""); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if !inTheRail(in, "radar") {
		t.Fatal("enabling did not put it in the menu")
	}

	if rec := in.request(t, "POST", "/api/v1/plugins/radar/disable", in.adminTok, ""); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if inTheRail(in, "radar") {
		t.Error("a disabled plugin is still contributing to the menu")
	}
}

// inTheRail asks the server what the menu holds, the same way the browser does.
func inTheRail(in *install, id string) bool {
	for _, item := range in.srv.pluginRT().nav {
		if item.From == id {
			return true
		}
	}
	return false
}

// navPlugin is a bundle that adds one entry to the rail and nothing else — no
// `needs:`, so it is templates alone and owes nobody a restart.
func navPlugin(t *testing.T, id string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), id+".zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	add := func(name, body string) {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("plugin.yaml", "schema: 1\nid: "+id+"\nname: "+id+"\nversion: 1.0.0\n"+
		"host:\n  contract: 1\n"+
		"nav:\n  - label: "+id+"\n    href: /p/"+id+"/home\n"+
		"pages:\n  - path: /p/"+id+"/home\n    template: "+id+".page.home\n")
	add("templates/t.html", `{{define "`+id+`.page.home"}}hello{{end}}`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
