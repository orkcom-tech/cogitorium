package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"

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
	if !spec.Network {
		a = append(a, "--network", "none")
	}
	for k, v := range spec.Env {
		a = append(a, "-e", k+"="+v)
	}
	a = append(a, d.Image, spec.Command)
	return append(a, spec.Args...)
}

const workDir = "/work"

// create makes the container and copies the code into it, returning its id.
func (d *Docker) create(ctx context.Context, spec Spec, interactive bool) (string, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, d.bin, d.createArgs(spec, interactive)...)
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
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
		writeErr := writePayload(stdin, spec.Dir, workDir)
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

func (d *Docker) remove(ctx context.Context, id string) {
	if err := exec.CommandContext(ctx, d.bin, "rm", "-f", id).Run(); err != nil {
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
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

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

	if res.TimedOut {
		return res, fmt.Errorf("timed out after %ds", timeout)
	}
	// A non-zero exit is the program's own result, not a failure to run.
	if runErr != nil && res.ExitCode < 0 {
		return res, fmt.Errorf("could not run container: %w: %s", runErr, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}
