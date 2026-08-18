package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/update"
)

// Neither dangerous capability had a regression test on its default. Both are
// off-by-default by ABSENCE from Defaults(), which is exactly the kind of
// property that a well-meaning "let's fill in every field" refactor flips
// without anyone noticing.
func TestDangerousCapabilitiesAreOffByDefault(t *testing.T) {
	d := Defaults()
	if d.Terminal {
		t.Error("the terminal defaults to on: that is interactive code execution over HTTP")
	}
	if d.Egress {
		t.Error("egress defaults to on: agents could reach the internet on a fresh install")
	}
	if d.EgressKey != "" {
		t.Error("a credential is baked into the defaults")
	}
	// Not dangerous in the way the others are — nothing about this install is
	// ever sent — but it is the first outbound request this server makes on
	// its own behalf, and the README promises the product fetches nothing at
	// runtime. "ask" keeps that promise true until somebody says otherwise;
	// "on" would quietly make it false.
	// A scrape port is a new thing listening on somebody's machine. Starting
	// one without being asked is a decision that is not ours to make.
	if d.MetricsListen != "" {
		t.Errorf("metrics_listen defaults to %q; the endpoint must be asked for", d.MetricsListen)
	}
	if d.UpdateCheck != update.ModeAsk {
		t.Errorf("update_check defaults to %q; it must be %q, or a fresh install talks to GitHub "+
			"without anybody having agreed to it", d.UpdateCheck, update.ModeAsk)
	}
}

func TestEgressEnvIsAStrictParseSoZeroTurnsItOff(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off", "yes", "TRUE-ish", " 1"} {
		t.Setenv("COGITORIUM_EGRESS", v)
		cfg, err := Load("", "")
		if err != nil {
			t.Fatalf("loading with COGITORIUM_EGRESS=%q: %v", v, err)
		}
		want := false
		if cfg.Egress != want {
			t.Errorf("COGITORIUM_EGRESS=%q produced Egress=%v; only \"1\" and \"true\" may enable it", v, cfg.Egress)
		}
	}
	for _, v := range []string{"1", "true", "TRUE", "True"} {
		t.Setenv("COGITORIUM_EGRESS", v)
		cfg, err := Load("", "")
		if err != nil {
			t.Fatalf("loading with COGITORIUM_EGRESS=%q: %v", v, err)
		}
		if !cfg.Egress {
			t.Errorf("COGITORIUM_EGRESS=%q did not enable egress", v)
		}
	}
}

// The seeded admin password is refused at startup when it is weaker than a
// person would be allowed to choose. One floor, from internal/identity, so a
// credential supplied by a deployment cannot be the weak one.
func TestAShortSeededAdminPasswordStopsStartup(t *testing.T) {
	t.Setenv("COGITORIUM_ADMIN_PASSWORD", "short")
	if _, err := Load("", ""); err == nil {
		t.Fatal("a five-character COGITORIUM_ADMIN_PASSWORD was accepted at startup")
	}

	t.Setenv("COGITORIUM_ADMIN_PASSWORD", "correct-horse-battery")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatalf("a long enough password was refused: %v", err)
	}
	if cfg.AdminPassword != "correct-horse-battery" {
		t.Errorf("AdminPassword is %q, want the value from the environment", cfg.AdminPassword)
	}
}

// It is environment-only, like the token and the secret key: on Kubernetes the
// config file is a ConfigMap, and a ConfigMap is not a secret. A yaml tag would
// make putting it there possible by mistake.
func TestTheSeededAdminPasswordCannotComeFromTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cogitorium.yaml")
	if err := os.WriteFile(path, []byte("admin_password: from-the-config-file\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AdminPassword != "" {
		t.Errorf("a password in the config file was read as %q; it must come from the environment or not at all",
			cfg.AdminPassword)
	}
}
