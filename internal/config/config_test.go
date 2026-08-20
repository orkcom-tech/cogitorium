package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/update"
)

// These capabilities are off-by-default by ABSENCE from Defaults(), which is
// exactly the kind of property that a well-meaning "let's fill in every field"
// refactor flips without anyone noticing.
//
// The terminal used to be in this list and is deliberately no longer: it is on
// by default, and its own test below is the one that guards it.
func TestDangerousCapabilitiesAreOffByDefault(t *testing.T) {
	d := Defaults()
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

// The terminal is on by default and `terminal: false` must switch it off.
//
// The second half is the one worth having. The field is a *bool precisely
// because a plain bool cannot tell "the operator wrote false" from "the
// operator wrote nothing", and those mean opposite things here: somebody
// running a shared install writes `terminal: false` once and has every right
// to expect it holds. If this ever passes only its first half, that operator
// has a shell on their machine they explicitly refused.
func TestTheTerminalIsOnUnlessSomebodySaysOtherwise(t *testing.T) {
	if !Defaults().TerminalOn() {
		t.Error("the terminal defaults to off; it is meant to be there like the one in an editor")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "cogitorium.yaml")
	if err := os.WriteFile(path, []byte("terminal: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("loading a configuration that switches the terminal off: %v", err)
	}
	if cfg.TerminalOn() {
		t.Fatal("`terminal: false` was read as if nothing had been written: this install offers a " +
			"shell its operator refused")
	}

	// And the same through the environment, which is how a container is
	// configured and therefore how most shared installs will say it.
	t.Setenv("COGITORIUM_TERMINAL", "0")
	cfg, err = Load("", "")
	if err != nil {
		t.Fatalf("loading with COGITORIUM_TERMINAL=0: %v", err)
	}
	if cfg.TerminalOn() {
		t.Error("COGITORIUM_TERMINAL=0 left the terminal on")
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

// TestATrailingNewlineIsNotPartOfTheAdminPassword is the mistake that locks an
// operator out of their own cluster in silence.
//
// `kubectl create secret generic --from-file=admin-password=pw` stores the
// file, and a file an editor wrote ends in a newline. Kept, it is hashed into
// the credential — and then the operator types the password they chose and gets
// the same 401 a typo produces, with nothing anywhere saying why.
func TestATrailingNewlineIsNotPartOfTheAdminPassword(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\n\n"} {
		t.Setenv("COGITORIUM_ADMIN_PASSWORD", "correct-horse-battery"+ending)
		cfg, err := Load("", "")
		if err != nil {
			t.Fatalf("password ending in %q was refused: %v", ending, err)
		}
		if cfg.AdminPassword != "correct-horse-battery" {
			t.Errorf("password ending in %q was read as %q", ending, cfg.AdminPassword)
		}
	}

	// Only line endings. A trailing space could be somebody's actual choice,
	// and quietly changing it would be the same silent failure in reverse.
	t.Setenv("COGITORIUM_ADMIN_PASSWORD", "correct-horse-battery ")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AdminPassword != "correct-horse-battery " {
		t.Errorf("a trailing space was trimmed: %q", cfg.AdminPassword)
	}
}

// TestALongSeededAdminPasswordIsRefusedAtStartup: bcrypt hashes at most 72
// bytes and refuses rather than truncates, so a longer one is not a weak
// password — it is a pod that fails to start, on every start, reporting a
// hashing error rather than the variable that caused it.
func TestALongSeededAdminPasswordIsRefusedAtStartup(t *testing.T) {
	t.Setenv("COGITORIUM_ADMIN_PASSWORD", strings.Repeat("a", 73))
	if _, err := Load("", ""); err == nil {
		t.Fatal("a 73-byte admin password was accepted; the pod would crash-loop instead")
	}

	// Bytes, not characters: this is 30 characters and 90 bytes.
	t.Setenv("COGITORIUM_ADMIN_PASSWORD", strings.Repeat("パ", 30))
	if _, err := Load("", ""); err == nil {
		t.Fatal("a 30-character, 90-byte password was accepted")
	}

	t.Setenv("COGITORIUM_ADMIN_PASSWORD", strings.Repeat("a", 72))
	if _, err := Load("", ""); err != nil {
		t.Fatalf("exactly 72 bytes was refused: %v", err)
	}
}
