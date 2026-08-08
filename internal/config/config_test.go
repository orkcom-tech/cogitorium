package config

import "testing"

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
	if d.EgressApprovalBearer {
		t.Error("EgressApprovalBearer changed default; it is documented as off, and the audit " +
			"records which authentication each decision had precisely because of that")
	}
	if d.EgressKey != "" {
		t.Error("a credential is baked into the defaults")
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
