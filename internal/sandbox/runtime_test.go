package sandbox

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// Selecting the OCI runtime a gear's container uses.
//
// Cogitorium does not install gVisor or Kata; it names one and refuses a name
// the daemon does not have. These tests run against the real daemon, because
// the whole value of the feature is that Docker agrees — a mocked `docker
// info` would prove that a string survives a function call and nothing else.

func dockerOrSkip(t *testing.T) *Docker {
	t.Helper()
	d := NewDocker("", "")
	if d == nil {
		t.Skip("docker is not on PATH; this checks agreement with a real daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !d.Available(ctx) {
		t.Skip("the docker daemon does not answer; this checks agreement with a real one")
	}
	return d
}

// The daemon's own list, whatever it happens to be on this machine.
func TestTheDaemonNamesItsRuntimes(t *testing.T) {
	d := dockerOrSkip(t)
	got, err := d.Runtimes(t.Context())
	if err != nil {
		t.Fatalf("ask docker for its runtimes: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("docker reported no runtimes at all, which cannot be true of a daemon that is running")
	}
	// Every daemon has this one. If it is missing, the parse is wrong rather
	// than the daemon being exotic.
	if !slices.Contains(got, "runc") {
		t.Fatalf("no \"runc\" among %v — the info template is probably not being parsed", got)
	}
}

// A runtime the daemon has is accepted.
func TestARuntimeTheDaemonHasIsAccepted(t *testing.T) {
	d := dockerOrSkip(t)
	d.Runtime = "runc"
	if err := d.CheckRuntime(t.Context()); err != nil {
		t.Fatalf("runc is on every daemon and was refused: %v", err)
	}
}

// And one it does not have is refused — with the available names in the
// message, because "not found" without a list is a message that sends somebody
// to a search engine rather than to their daemon's configuration.
func TestARuntimeTheDaemonLacksIsRefusedAndSaysWhatItHas(t *testing.T) {
	d := dockerOrSkip(t)
	d.Runtime = "runsc-that-nobody-installed"
	err := d.CheckRuntime(t.Context())
	if err == nil {
		t.Fatal("a runtime this daemon does not have was accepted, so the check is decorative")
	}
	for _, want := range []string{"runsc-that-nobody-installed", "runc", "does not install"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %s", want, err)
		}
	}
}

// No runtime configured is not an error: that is the default and the common case.
func TestNoRuntimeConfiguredChecksNothing(t *testing.T) {
	d := dockerOrSkip(t)
	d.Runtime = ""
	if err := d.CheckRuntime(t.Context()); err != nil {
		t.Fatalf("an empty runtime should ask the daemon nothing at all: %v", err)
	}
}

// The flag reaches the container, and only when it was asked for.
func TestTheRuntimeFlagIsPassedOnlyWhenSet(t *testing.T) {
	with := (&Docker{Image: "img", Runtime: "runsc"}).createArgs(Spec{Command: "sh"}, false)
	if i := slices.Index(with, "--runtime"); i < 0 || with[i+1] != "runsc" {
		t.Fatalf("--runtime runsc is not in the create arguments: %v", with)
	}
	without := (&Docker{Image: "img"}).createArgs(Spec{Command: "sh"}, false)
	if slices.Contains(without, "--runtime") {
		t.Fatalf("--runtime was passed with none configured, which overrides the daemon's own default: %v", without)
	}
}

// The hardening is not lost when a runtime is named. This is the regression
// that would matter most and show least: a container that runs perfectly well
// under gVisor with its capabilities back.
func TestNamingARuntimeKeepsEveryOtherRestriction(t *testing.T) {
	args := strings.Join((&Docker{Image: "img", Runtime: "runsc"}).createArgs(Spec{Command: "sh"}, false), " ")
	for _, want := range []string{
		"--cap-drop=ALL", "--security-opt no-new-privileges", "--pids-limit 256",
		"--memory 512m", "--cpus 1", "--user 65534:65534", "--network none",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("%s is gone once a runtime is named: %s", want, args)
		}
	}
}

// And the whole thing runs. A flag Docker accepts in a create call is the only
// evidence that this feature works at all — the rest of these tests check a
// string, and a string is not a container.
func TestAGearActuallyRunsUnderAnExplicitRuntime(t *testing.T) {
	d := dockerOrSkip(t)
	if err := exec.CommandContext(t.Context(), "docker", "image", "inspect", DefaultImage).Run(); err != nil {
		t.Skipf("%s is not pulled here; this test runs work rather than pulling half a distribution", DefaultImage)
	}
	d.Image = DefaultImage
	d.Runtime = "runc" // the one every daemon has, named explicitly rather than defaulted

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	res, err := d.Run(ctx, Spec{Command: "sh", Args: []string{"-c", "echo runtime-ok"}, TimeoutSeconds: 60})
	if err != nil {
		t.Fatalf("running under an explicitly named runtime failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "runtime-ok") {
		t.Fatalf("the container ran but produced %q (exit %d, stderr %q)", res.Stdout, res.ExitCode, res.Stderr)
	}
}
