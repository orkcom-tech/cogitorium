package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gap that let a real bug through.
//
// --data was ignored and every install went to the default data directory,
// while the command printed the path the operator asked for. The store's own
// tests could not catch it: they call the store directly and never go through
// the flag, the config loader, or the assignment between them. So this drives
// the actual command.
func TestTheDataFlagDecidesWhereAPluginLands(t *testing.T) {
	dir := t.TempDir()
	bundle := writeBundle(t, "schema: 1\nid: radar\nname: Radar\nversion: 1.0.0\nhost:\n  contract: 1\n")

	run(t, "install", bundle, "--data", dir)

	if _, err := os.Stat(filepath.Join(dir, "plugins", "radar", "current")); err != nil {
		t.Fatalf("the plugin did not land in the directory that was asked for: %v", err)
	}
}

func TestInstallDoesNotEnable(t *testing.T) {
	dir := t.TempDir()
	bundle := writeBundle(t, "schema: 1\nid: radar\nname: Radar\nversion: 1.0.0\nhost:\n  contract: 1\n")

	run(t, "install", bundle, "--data", dir)
	if _, err := os.Stat(filepath.Join(dir, "plugins.order")); !os.IsNotExist(err) {
		t.Errorf("installing must not write an enable list: %v", err)
	}

	// Installing is not a decision, so enabling is refused until one is made.
	cmd := newPluginsCmds()
	cmd.SetArgs([]string{"enable", "radar", "--data", dir})
	cmd.SetOut(devNull{})
	cmd.SetErr(devNull{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("enabling an unapproved plugin must be refused")
	}

	run(t, "approve", "radar", "--data", dir)
	run(t, "enable", "radar", "--data", dir)
	b, err := os.ReadFile(filepath.Join(dir, "plugins.order"))
	if err != nil {
		t.Fatalf("enabling must write the list: %v", err)
	}
	if !strings.Contains(string(b), "radar") {
		t.Errorf("the list does not name the plugin: %s", b)
	}
}

// Every mutating verb has to reach the same directory, or one of them quietly
// works on somebody else's install.
func TestEveryVerbHonoursTheDataFlag(t *testing.T) {
	dir := t.TempDir()
	bundle := writeBundle(t, "schema: 1\nid: radar\nname: Radar\nversion: 1.0.0\nhost:\n  contract: 1\n")

	run(t, "install", bundle, "--data", dir)
	run(t, "approve", "radar", "--data", dir)
	run(t, "enable", "radar", "--data", dir)
	run(t, "order", "radar", "--data", dir)
	run(t, "check", "--data", dir)
	run(t, "list", "--data", dir)
	run(t, "disable", "radar", "--data", dir)
	run(t, "remove", "radar", "--data", dir)

	if _, err := os.Stat(filepath.Join(dir, "plugins", "radar")); !os.IsNotExist(err) {
		t.Errorf("remove did not reach the directory that was asked for: %v", err)
	}
}

func TestAnInvalidBundleIsRefusedAndLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	bundle := writeBundle(t, "schema: 1\nid: X\nversion: nope\nhost:\n  contract: 1\n")

	cmd := newPluginsCmds()
	cmd.SetArgs([]string{"install", bundle, "--data", dir})
	cmd.SetOut(devNull{})
	cmd.SetErr(devNull{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("an invalid manifest must fail the command")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "plugins"))
	if len(entries) != 0 {
		t.Errorf("a refused bundle left something behind: %v", entries)
	}
}

func run(t *testing.T, args ...string) {
	t.Helper()
	cmd := newPluginsCmds()
	cmd.SetArgs(args)
	cmd.SetOut(devNull{})
	cmd.SetErr(devNull{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("plugins %s: %v", strings.Join(args, " "), err)
	}
}

func writeBundle(t *testing.T, manifest string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.zip")
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
	add("plugin.yaml", manifest)
	add("templates/p.html", `{{define "radar.page.home"}}hi{{end}}`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
