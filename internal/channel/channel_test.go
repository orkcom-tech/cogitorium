package channel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecProbeSucceedsOnAnOrdinaryDirectory(t *testing.T) {
	dir := t.TempDir()
	ok, refusal := execProbe(dir, Local)
	if !ok {
		t.Fatalf("a temp directory should be executable, got refusal: %s", refusal)
	}
	if refusal != "" {
		t.Errorf("a successful probe must not carry a refusal, got %q", refusal)
	}
}

// The probe has to clean up after itself. A server that left a probe directory
// behind on every start would grow one per restart forever.
func TestExecProbeLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	execProbe(dir, Local)
	if _, err := os.Stat(filepath.Join(dir, "probe")); !os.IsNotExist(err) {
		t.Errorf("the probe directory survived the probe: %v", err)
	}
}

// A umask that strips the executable bit must not be reported as a noexec
// mount. This is the difference between "your storage is hardened" and "your
// service unit sets umask 077", and telling an operator the wrong one sends
// them to the wrong team.
func TestExecProbeSurvivesARestrictiveUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("umask is not a Windows concept")
	}
	old := syscallUmask(0o077)
	defer syscallUmask(old)

	dir := t.TempDir()
	ok, refusal := execProbe(dir, Local)
	if !ok {
		t.Fatalf("umask 077 must not read as noexec; got refusal: %s", refusal)
	}
}

func TestExecProbeRefusesAMissingDataDir(t *testing.T) {
	ok, refusal := execProbe("", Local)
	if ok {
		t.Fatal("an empty data directory cannot be probed successfully")
	}
	if refusal == "" {
		t.Error("a failed probe must explain itself")
	}
}

// Every refusal is read by a person deciding what to do next, so it must name
// the directory and say what still works. A bare errno is not an explanation.
func TestExecRefusalsNameThePathAndWhatSurvives(t *testing.T) {
	for _, k := range []Kind{Kubernetes, Docker, Local, Service, Desktop} {
		msg := execRefusal("/var/lib/cogitorium", k, os.ErrPermission)
		if !strings.Contains(msg, "/var/lib/cogitorium") {
			t.Errorf("%s refusal does not name the data directory: %s", k, msg)
		}
		if !strings.Contains(msg, "WebAssembly") {
			t.Errorf("%s refusal does not say which plugins still work: %s", k, msg)
		}
	}
}

// Kind is compiled into refusal wording and into the tier tables. A rename
// would silently change which plugins an install believes it can run.
func TestKindValuesAreStable(t *testing.T) {
	want := map[Kind]string{
		Kubernetes: "kubernetes",
		Docker:     "docker",
		Service:    "service",
		Desktop:    "desktop",
		Local:      "local",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("Kind %v changed value to %q; tier availability tables key on this", k, string(k))
		}
	}
}

func TestDetectLibcIsOnlyMeaningfulOnLinux(t *testing.T) {
	got := detectLibc()
	if runtime.GOOS != "linux" {
		if got != LibcNone {
			t.Errorf("libc must be unset off Linux, got %q", got)
		}
		return
	}
	if got != Musl && got != Glibc {
		t.Errorf("on Linux libc must be decided, got %q", got)
	}
}

func TestProfileStringCarriesTheDecidingFacts(t *testing.T) {
	p := Profile{Kind: Kubernetes, OS: "linux", Arch: "amd64", Libc: Musl, CanExecFromData: false}
	s := p.String()
	for _, want := range []string{"kubernetes", "linux", "amd64", "musl", "exec-from-data=no"} {
		if !strings.Contains(s, want) {
			t.Errorf("the startup line omits %q: %s", want, s)
		}
	}
}

func TestMarkDesktopWins(t *testing.T) {
	old := desktop
	defer func() { desktop = old }()

	desktop = true
	if got := detectKind(); got != Desktop {
		t.Errorf("the desktop shell knows what it is; got %q", got)
	}
}
