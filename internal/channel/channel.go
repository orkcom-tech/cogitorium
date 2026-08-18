// Package channel answers one question the plugin system asks constantly:
// where is this server actually running, and what can a plugin do here.
//
// The answer is not a configuration value. An operator who mistyped it would
// be told a plugin works and then watch it fail at the moment it mattered,
// so every field here is either read from a real signal or proved by doing
// the thing and seeing whether it worked.
//
// The distinction that motivates the whole package: a plugin that is only
// templates and a plugin that is a WebAssembly module both run everywhere by
// construction, because a template is data and the two engines that consume
// them are compiled into this binary. Everything else — a fetched interpreter,
// a container image, a native binary — is available on some channels and not
// on others, and the difference has to be discovered before an operator is
// asked to approve an install, not after.
package channel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Kind names the shape of the deployment. It decides nothing on its own —
// every capability is probed — but it selects the wording of a refusal, and
// an operator reading "the data volume is mounted noexec" wants to know
// whether they are looking at a Kubernetes PersistentVolumeClaim or a laptop.
type Kind string

const (
	// Kubernetes is a pod. The data directory is a volume somebody's storage
	// class provided, which is the case where noexec is most likely to be
	// deliberate.
	Kubernetes Kind = "kubernetes"
	// Docker is a container that is not a Kubernetes pod — the compose file,
	// or a plain docker run.
	Docker Kind = "docker"
	// Service is a systemd unit. Restart-to-activate is free here: the process
	// exits and systemd starts it again.
	Service Kind = "service"
	// Desktop is the webview shell. It is a different main package, so it says
	// so rather than being guessed at.
	Desktop Kind = "desktop"
	// Local is a binary somebody downloaded and ran. Nothing restarts it but
	// itself, which is why the restart controller exists.
	Local Kind = "local"
)

// Libc is which C library will actually execute a fetched binary. This is not
// cosmetic and it is not GOOS/GOARCH: a glibc build does not start on the
// Alpine image, and the failure is a missing-interpreter error that says
// nothing about the cause. A fetched runtime is keyed on this.
type Libc string

const (
	Musl  Libc = "musl"
	Glibc Libc = "glibc"
	// LibcNone is every platform where the question does not arise.
	LibcNone Libc = ""
)

// Profile is what this install can do, decided once at startup.
//
// It is deliberately a value rather than an interface. There is one true
// answer per process, it cannot change while the process lives — a storage
// class does not remount under a running pod — and code that takes a Profile
// cannot accidentally re-probe in a loop.
type Profile struct {
	Kind Kind
	OS   string
	Arch string
	// Libc is the server's own. It governs a runtime fetched into the data
	// directory, which this process execs itself.
	Libc Libc

	// DataDir is the directory the probes were run against, so a refusal can
	// name the path an operator would go looking at.
	DataDir string

	// CanExecFromData reports whether a file written into the data directory
	// can be executed. False is the single most consequential fact in this
	// package: it is what makes a fetched interpreter impossible, and it is
	// almost always somebody's deliberate hardening rather than a fault.
	CanExecFromData bool
	// ExecRefusal explains CanExecFromData being false in one sentence naming
	// the path. Empty when execution works.
	ExecRefusal string
}

// desktop is set by the desktop shell's main package. A compiled-in fact
// rather than a probe, because the desktop binary is a separate main and
// knows what it is without having to guess from its environment.
var desktop bool

// MarkDesktop records that this process is the desktop shell. It must be
// called before Detect; the desktop main does so in its own init.
func MarkDesktop() { desktop = true }

var (
	once   sync.Once
	cached Profile
)

// Detect works out the profile, running the probes. The result is cached for
// the life of the process: the probes write and execute a real file, and
// doing that on every question would be a syscall storm for an answer that
// cannot change.
func Detect(dataDir string) Profile {
	once.Do(func() { cached = probe(dataDir) })
	return cached
}

func probe(dataDir string) Profile {
	p := Profile{
		Kind:    detectKind(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Libc:    detectLibc(),
		DataDir: dataDir,
	}
	p.CanExecFromData, p.ExecRefusal = execProbe(dataDir, p.Kind)
	return p
}

// detectKind reads the environment for signals that are set by the thing
// running us, never by a person configuring us.
func detectKind() Kind {
	if desktop {
		return Desktop
	}
	// The kubelet injects this into every pod. Its presence is not a guess.
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return Kubernetes
	}
	if inContainer() {
		return Docker
	}
	// systemd sets both. INVOCATION_ID alone can survive into a child shell
	// somebody opened from a unit, so both are required — a person debugging
	// in a terminal is not a service.
	if os.Getenv("INVOCATION_ID") != "" && os.Getenv("JOURNAL_STREAM") != "" {
		return Service
	}
	return Local
}

func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// cgroup v1 names the container runtime in the path. On cgroup v2 this
	// file is a single line that usually says nothing useful, which is why
	// /.dockerenv is checked first rather than instead.
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	for _, marker := range []string{"docker", "containerd", "kubepods", "podman"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// detectLibc asks the filesystem rather than running ldd, because ldd is a
// shell script on glibc and absent on musl — the tool that would answer the
// question is itself the thing being asked about.
func detectLibc() Libc {
	if runtime.GOOS != "linux" {
		return LibcNone
	}
	// musl's dynamic loader is named for the architecture and lives at a
	// fixed path. Its presence is what an ELF's PT_INTERP would point at.
	matches, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	if len(matches) > 0 {
		return Musl
	}
	return Glibc
}

// execProbe writes a tiny executable into the data directory and runs it.
//
// This is done by doing rather than by reading /proc/mounts, and the reason is
// that the mount options are not the only way execution gets denied: a
// Windows AppLocker or WDAC policy refuses by policy with the filesystem
// perfectly ordinary, and an SELinux label can refuse on a mount that reads as
// exec. The only claim worth making is "we executed a file here and it ran".
func execProbe(dataDir string, kind Kind) (bool, string) {
	if dataDir == "" {
		return false, "no data directory was configured, so nothing could be probed"
	}
	dir := filepath.Join(dataDir, "probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Sprintf("the data directory %s could not be written to (%v), "+
			"so a fetched runtime could not be stored there either", dataDir, err)
	}
	defer os.RemoveAll(dir)

	name, body, mode := probeScript()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		return false, fmt.Sprintf("writing a test program to %s failed (%v)", dir, err)
	}
	// WriteFile's mode is masked by umask, so the executable bit is set again
	// explicitly. A umask of 077 in a service unit would otherwise make this
	// probe report noexec on a filesystem that is perfectly fine.
	if err := os.Chmod(path, mode); err != nil {
		return false, fmt.Sprintf("making a test program executable in %s failed (%v)", dir, err)
	}

	if err := exec.Command(path).Run(); err != nil {
		return false, execRefusal(dataDir, kind, err)
	}
	return true, ""
}

// probeScript is the smallest thing that proves execution works on this
// platform. It is deliberately not a compiled binary: writing one would mean
// carrying six of them, and the question being asked — will the kernel let us
// exec a file from this directory — is answered by a script just as well,
// because the same mount option and the same policy gate both.
func probeScript() (name, body string, mode os.FileMode) {
	if runtime.GOOS == "windows" {
		return "probe.bat", "@echo off\r\nexit /b 0\r\n", 0o755
	}
	return "probe.sh", "#!/bin/sh\nexit 0\n", 0o755
}

func execRefusal(dataDir string, kind Kind, err error) string {
	base := fmt.Sprintf("a test program written to %s could not be executed (%v)", dataDir, err)

	var perm error = os.ErrPermission
	denied := errors.Is(err, perm) || strings.Contains(err.Error(), "permission denied")
	if !denied {
		return base + ". Plugins that need a fetched runtime will be refused here"
	}

	switch kind {
	case Kubernetes:
		return base + ". This is what a data volume mounted noexec looks like: the storage class or " +
			"the pod's securityContext denied execution. Plugins whose backend is a fetched " +
			"interpreter cannot run here — templates and WebAssembly plugins are unaffected"
	case Docker:
		return base + ". The data volume denies execution — a noexec mount option, or a tmpfs mounted " +
			"noexec. Plugins whose backend is a fetched interpreter cannot run here; templates and " +
			"WebAssembly plugins are unaffected"
	default:
		return base + ". Execution from this directory is denied, either by a noexec mount or by a " +
			"policy such as AppLocker. Plugins whose backend is a fetched interpreter cannot run " +
			"here; templates and WebAssembly plugins are unaffected"
	}
}

// String renders the profile as one line for the startup log. An operator who
// later files "my plugin will not install" should be able to find this line
// and have most of the answer.
func (p Profile) String() string {
	s := fmt.Sprintf("channel=%s os=%s arch=%s", p.Kind, p.OS, p.Arch)
	if p.Libc != LibcNone {
		s += " libc=" + string(p.Libc)
	}
	if p.CanExecFromData {
		s += " exec-from-data=yes"
	} else {
		s += " exec-from-data=no"
	}
	return s
}
