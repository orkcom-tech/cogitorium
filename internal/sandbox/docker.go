package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"

	"strings"
	"time"
)

// Docker runs work in a container. This is the isolation that matters: the
// process gets its own filesystem view, so the server's database — and the
// provider API keys in it — are simply not there to read.
//
// It is also the backend that maps onto the planned Kubernetes Jobs, so
// what is proven here carries over rather than being rewritten.
type Docker struct {
	// Image must contain the interpreters gears use. Kept configurable
	// because a team will want their own with their dependencies baked in.
	Image string
	bin   string
}

const (
	DefaultImage   = "python:3.12-alpine"
	defaultTimeout = 60
)

// NewDocker returns a Docker runner, or nil if Docker is not usable here.
// The caller decides what to do about that; this package does not silently
// fall back to running unsandboxed.
func NewDocker(image string) *Docker {
	bin, err := exec.LookPath("docker")
	if err != nil {
		slog.Info("docker not found; sandboxed execution unavailable")
		return nil
	}
	if image == "" {
		image = DefaultImage
	}
	return &Docker{Image: image, bin: bin}
}

func (d *Docker) Name() string   { return "docker:" + d.Image }
func (d *Docker) Isolated() bool { return true }

// Available reports whether the daemon actually answers — the binary being
// on PATH is not the same as Docker running.
func (d *Docker) Available(ctx context.Context) bool {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(probe, d.bin, "version", "--format", "{{.Server.Version}}").Run() == nil
}

// createArgs builds the container. Every flag is load-bearing: capabilities
// dropped, privilege escalation refused, no network unless asked, an
// unprivileged user, and resource ceilings so a runaway starves nothing.
//
// The code is copied in rather than bind-mounted. Bind mounts tie execution
// to a daemon that can see the host's filesystem, which rules out a remote
// or rootless daemon and depends on file-sharing settings; copying works
// everywhere and is the same shape a Kubernetes Job will need.
func (d *Docker) createArgs(spec Spec, interactive bool) []string {
	a := []string{
		"create", "-i",
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "256",
		"--memory", "512m",
		"--cpus", "1",
		"--user", "65534:65534",
		"--workdir", workDir,
	}
	if interactive {
		a = append(a, "-t")
	}
	if spec.Network {
		// The gate that checks and records a granted gear's traffic runs in the
		// server's own process, so the container has to have a name for the
		// machine it is running on. Docker Desktop provides this name already;
		// on Linux it does not exist unless it is asked for, and a gear whose
		// proxy address does not resolve fails with a DNS error that says
		// nothing about why.
		a = append(a, "--add-host", HostAlias+":host-gateway")
	} else {
		a = append(a, "--network", "none")
	}
	for k, v := range spec.Env {
		a = append(a, "-e", k+"="+v)
	}
	a = append(a, d.Image, spec.Command)
	return append(a, spec.Args...)
}

const workDir = "/work"

// cliTimeout bounds a docker CLI call that is plumbing rather than the gear's
// own work: create, cp, rm. The gear's timeout covers `start`, where the code
// actually runs; these have no business taking any length of time at all, and
// a daemon that is not answering must fail rather than wait.
const cliTimeout = 30 * time.Second

// dockerCLI runs one docker command with a deadline and, more importantly, a
// WaitDelay.
//
// Every call here already used CommandContext, so cancelling the context did
// kill the process — and Wait still blocked forever in awaitGoroutines,
// draining stdout and stderr pipes that a surviving descendant of the docker
// CLI still held open. Observed, not theorised: a wedged daemon left `docker
// create` hanging inside a gear call, which held the workspace's one-run latch
// for as long as the server lived, so every later delivery to that workspace
// answered 429 with nothing running. One sick daemon closed a door permanently.
//
// WaitDelay is the same medicine already applied to gear subprocesses: once the
// context is done, give the pipes a moment and then give up on them. Losing the
// tail of a killed command's output is the correct trade against never
// returning.
func (d *Docker) cli(ctx context.Context, stdout, stderr *bytes.Buffer, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, d.bin, args...)
	cmd.WaitDelay = 5 * time.Second
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}
	return cmd.Run()
}

// create makes the container and copies the code into it, returning its id.
func (d *Docker) create(ctx context.Context, spec Spec, interactive bool) (string, error) {
	var out, errOut bytes.Buffer
	if err := d.cli(ctx, &out, &errOut, d.createArgs(spec, interactive)...); err != nil {
		return "", fmt.Errorf("create container: %w: %s", err, strings.TrimSpace(errOut.String()))
	}
	id := strings.TrimSpace(out.String())

	if spec.Dir != "" {
		// Streamed as a tar rather than `docker cp <dir>/.`, so the payload's
		// ownership and modes are ours to state instead of the host's to leak.
		// See payload.go: the plain copy is why the sandbox could not enter a
		// subdirectory it had been handed, and why it owned nothing it was
		// given and so could not write at all.
		cp := exec.CommandContext(ctx, d.bin, "cp", "-", id+":/")
		var cpErr bytes.Buffer
		cp.Stderr = &cpErr
		stdin, err := cp.StdinPipe()
		if err != nil {
			d.remove(context.WithoutCancel(ctx), id)
			return "", fmt.Errorf("open the payload stream: %w", err)
		}
		if err := cp.Start(); err != nil {
			d.remove(context.WithoutCancel(ctx), id)
			return "", fmt.Errorf("copy code into container: %w", err)
		}
		writeErr := writePayload(stdin, spec.Dir, workDir, payloadOwner{writable: spec.Writable, out: spec.Out})
		// Close before Wait either way: docker only finishes once the stream
		// ends, so returning early on a write error would deadlock.
		closeErr := stdin.Close()
		if err := cp.Wait(); err != nil {
			d.remove(context.WithoutCancel(ctx), id)
			return "", fmt.Errorf("copy code into container: %w: %s", err, strings.TrimSpace(cpErr.String()))
		}
		if writeErr != nil {
			d.remove(context.WithoutCancel(ctx), id)
			return "", fmt.Errorf("build the payload: %w", writeErr)
		}
		if closeErr != nil {
			d.remove(context.WithoutCancel(ctx), id)
			return "", fmt.Errorf("finish the payload stream: %w", closeErr)
		}
	}
	return id, nil
}

// collect copies the writable subdirectory back out of the container, into the
// place on the host that the process saw as it: <Dir>/<Out>. The caller made
// that directory empty before the run, so what is in it afterwards is exactly
// what the process left behind — and a caller with no sandbox reads the same
// directory without any of this happening, which is why the two backends need
// no separate handling further up.
//
// The trailing "/." is the documented form for "the contents of this directory,
// not the directory itself", and is what puts the files where the caller
// expects rather than one level down.
//
// Extraction is docker's own, which refuses an entry whose name escapes the
// destination; and the daemon builds the archive by walking real directory
// entries, so no member can be named ".." in the first place. What CAN come
// across is a symlink the process created, pointing anywhere it likes —
// including at the host's own files. That is not defended here: it is defended
// where it can be, in the caller that walks the collected directory and takes
// nothing but regular files.
func (d *Docker) collect(ctx context.Context, id string, spec Spec) error {
	if spec.Out == "" {
		return nil
	}
	var errOut bytes.Buffer
	if err := d.cli(ctx, nil, &errOut, "cp",
		id+":"+workDir+"/"+spec.Out+"/.", filepath.Join(spec.Dir, spec.Out)); err != nil {
		return fmt.Errorf("could not copy %s/ back out of the sandbox, so any files it holds were lost: %w: %s",
			spec.Out, err, strings.TrimSpace(errOut.String()))
	}
	return nil
}

func (d *Docker) remove(ctx context.Context, id string) {
	if err := d.cli(ctx, nil, nil, "rm", "-f", id); err != nil {
		slog.Warn("could not remove sandbox container", "id", id, "err", err)
	}
}

func (d *Docker) Run(ctx context.Context, spec Spec) (Result, error) {
	timeout := spec.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	id, err := d.create(runCtx, spec, false)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	defer d.remove(context.WithoutCancel(ctx), id)

	cmd := exec.CommandContext(runCtx, d.bin, "start", "-a", "-i", id)
	// The gear's timeout stops the container; this stops the WAIT on it. They
	// are not the same thing: killing the CLI leaves Wait draining pipes that a
	// surviving descendant still holds, and the run's own deadline expiring is
	// exactly when that happens. Without this a timeout does not time out.
	cmd.WaitDelay = 5 * time.Second
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	}
	var stdout, stderr bytes.Buffer
	// The buffers still collect everything, so the recorded run is byte for
	// byte what it always was; the tap is additional. Without it the operator
	// watches a spinner for the whole of a sixty-second gear and then gets the
	// output all at once, which tells them nothing about where it got stuck.
	cmd.Stdout = tap(&stdout, "stdout", spec.OnOutput)
	cmd.Stderr = tap(&stderr, "stderr", spec.OnOutput)

	start := time.Now()
	runErr := cmd.Run()
	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
		TimedOut: runCtx.Err() == context.DeadlineExceeded,
	}
	slog.Info("sandboxed run", "backend", d.Name(), "exit_code", res.ExitCode,
		"timed_out", res.TimedOut, "duration_ms", time.Since(start).Milliseconds())

	// Collected whatever the exit code was, and whether or not it timed out: a
	// gear that wrote its result and then failed still wrote its result, and the
	// caller is owed the truth about what is there. The container still exists —
	// removal is deferred above — and this must happen before it goes.
	collectErr := d.collect(context.WithoutCancel(ctx), id, spec)

	if res.TimedOut {
		return res, fmt.Errorf("timed out after %ds", timeout)
	}
	if collectErr != nil {
		return res, collectErr
	}
	// A non-zero exit is the program's own result, not a failure to run.
	if runErr != nil && res.ExitCode < 0 {
		return res, fmt.Errorf("could not run container: %w: %s", runErr, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}

// tap returns a writer that fills buf and, when on is set, reports each chunk
// as it arrives. Chunks are whatever the pipe delivered — no line buffering,
// because a gear that prints a progress bar without newlines is exactly the
// one worth watching.
func tap(buf *bytes.Buffer, stream string, on func(string, string)) io.Writer {
	if on == nil {
		return buf
	}
	return io.MultiWriter(buf, writerFunc(func(p []byte) (int, error) {
		on(stream, string(p))
		return len(p), nil
	}))
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
