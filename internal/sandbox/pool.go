package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Warm containers: the one thing in this package that trades isolation for
// latency, which is why it is off unless an operator turns it on.
//
// Creating and destroying a container costs a few hundred milliseconds. For a
// gear that runs for a minute that is noise; for one that answers in two
// hundred milliseconds it is most of the wall clock. A pool keeps containers
// alive and hands a run one instead.
//
// What that costs is concrete, not theoretical. A container that has already
// run something is not a fresh machine: whatever the last run left outside its
// payload — a file in /tmp, a package it installed, a process it started — is
// still there. The payload directory is destroyed and rebuilt between runs, so
// no gear reads another's code or output, but /tmp is shared and the machine
// has a history.
//
// So the rule is narrow, and it is the caller's to state rather than this
// package's to guess: a run is pooled only when it says it may be
// (Spec.Reusable). The executor sets that false for any gear that was given
// named values or the network — exactly the runs that could leave a credential
// behind, and exactly the ones where a shared /tmp would matter.
//
// A container is also retired rather than returned when a run times out, since
// what timed out is still in there.

// warmRuns is how many runs one container serves before it is retired.
//
// Not unbounded: a long-lived container accumulates whatever every run before
// it left, and "it has been reused four hundred times" is not a machine anybody
// reasoned about. Twenty is enough to make creation a rounding error on a busy
// install and small enough that a leak is bounded.
const warmRuns = 20

// warmIdle is how long a pooled container lives with nothing to do. A pool that
// outlived the burst that filled it would hold memory on an idle machine for no
// reason.
const warmIdle = 10 * time.Minute

type warm struct {
	id    string
	image string
	runs  int
}

type pool struct {
	mu    sync.Mutex
	size  int
	idle  []*warm
	timer map[string]*time.Timer
}

func newPool(size int) *pool {
	if size <= 0 {
		return nil
	}
	return &pool{size: size, timer: map[string]*time.Timer{}}
}

// take returns a warm container for this image, or nil if there is none idle.
func (p *pool) take(image string) *warm {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, w := range p.idle {
		if w.image != image {
			continue
		}
		p.idle = append(p.idle[:i], p.idle[i+1:]...)
		if t, ok := p.timer[w.id]; ok {
			t.Stop()
			delete(p.timer, w.id)
		}
		return w
	}
	return nil
}

// put offers a container back. It reports whether the pool took it; false means
// the caller must remove it, and the caller is the only one that can.
func (p *pool) put(w *warm, retire func(string)) bool {
	if p == nil || w == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.runs >= warmRuns || len(p.idle) >= p.size {
		return false
	}
	p.idle = append(p.idle, w)
	p.timer[w.id] = time.AfterFunc(warmIdle, func() {
		if p.drop(w.id) {
			retire(w.id)
		}
	})
	return true
}

// drop removes one container from the idle set, reporting whether it was still
// there — so an idle timer and a taker cannot both act on the same container.
func (p *pool) drop(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, w := range p.idle {
		if w.id == id {
			p.idle = append(p.idle[:i], p.idle[i+1:]...)
			delete(p.timer, id)
			return true
		}
	}
	return false
}

// drain empties the pool and returns what was in it, for shutdown.
func (p *pool) drain() []*warm {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, t := range p.timer {
		t.Stop()
		delete(p.timer, id)
	}
	out := p.idle
	p.idle = nil
	return out
}

// warmable reports whether this run may be given a container that has already
// run something.
//
// Four conditions, and only the second is a policy. Pooling is on. The run said
// it is reusable. It asked for no network — not a policy but a fact, since a
// container's network mode is fixed when it is created, so a pooled container
// cannot become a granted one. And its payload is writable.
//
// That last one is the price of not weakening the container. A read-only
// payload is root-owned, which is what makes it read-only (see payload.go), and
// removing it between runs would need a root that can override file
// permissions — CAP_DAC_OVERRIDE, dropped along with everything else. Adding it
// back to reuse a container would be trading the sandbox for latency rather
// than trading a machine's history for it. So the file-carrying call keeps
// getting a fresh container, and the ordinary one — which is what a pool is for
// — does not.
func (d *Docker) warmable(spec Spec) bool {
	return d.pool != nil && spec.Reusable && !spec.Network && spec.Writable
}

// warmFor gets a container ready to take this run's payload: one from the pool,
// or a new one started for the purpose.
func (d *Docker) warmFor(ctx context.Context, image string) (*warm, error) {
	if w := d.pool.take(image); w != nil {
		// Confirm it is still running before handing it out. A daemon restart,
		// an operator's `docker rm`, or the OOM killer would otherwise turn a
		// pooled container into a gear that fails for a reason nothing in this
		// install can explain.
		var out bytes.Buffer
		if err := d.cli(ctx, &out, nil, "inspect", "-f", "{{.State.Running}}", w.id); err == nil &&
			strings.TrimSpace(out.String()) == "true" {
			return w, nil
		}
		slog.Info("a pooled container had gone; starting another", "id", w.id)
		d.remove(context.WithoutCancel(ctx), w.id)
	}

	// The same flags as a per-run container, minus the payload and the command.
	// Anything weaker here would make a pooled run a differently confined run,
	// which is the one thing this must not be.
	a := []string{"run", "-d", "--network", "none"}
	a = append(a, d.confinement()...)
	if d.Runtime != "" {
		a = append(a, "--runtime", d.Runtime)
	}
	a = append(a, "--entrypoint", "sh", image, "-c", "while :; do sleep 3600; done")

	var out, errOut bytes.Buffer
	if err := d.cli(ctx, &out, &errOut, a...); err != nil {
		return nil, fmt.Errorf("start a warm container from %s: %w: %s — an image that pooling can hold "+
			"has to have a shell, so an install running FROM scratch gears cannot use sandbox_pool",
			image, err, strings.TrimSpace(errOut.String()))
	}
	return &warm{id: strings.TrimSpace(out.String()), image: image}, nil
}

// runWarm executes one gear inside a container that is already running.
//
// The payload is destroyed before and after rather than only after: "after"
// alone trusts the previous run to have finished tidying, and a run that was
// killed did not.
func (d *Docker) runWarm(ctx context.Context, runCtx context.Context, w *warm, spec Spec) (Result, error) {
	if err := d.clearPayload(ctx, w.id); err != nil {
		return Result{ExitCode: -1}, err
	}
	if spec.Dir != "" {
		if err := d.copyPayload(runCtx, w.id, spec); err != nil {
			return Result{ExitCode: -1}, err
		}
	}

	a := []string{"exec", "-i", "--user", "65534:65534", "--workdir", workDir}
	for k, v := range spec.Env {
		a = append(a, "-e", k+"="+v)
	}
	a = append(a, w.id, spec.Command)
	a = append(a, spec.Args...)

	cmd := exec.CommandContext(runCtx, d.bin, a...)
	cmd.WaitDelay = 5 * time.Second
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	}
	var stdout, stderr bytes.Buffer
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
	w.runs++
	slog.Info("sandboxed run", "backend", d.Name(), "warm", true, "container_runs", w.runs,
		"exit_code", res.ExitCode, "timed_out", res.TimedOut, "duration_ms", time.Since(start).Milliseconds())

	collectErr := d.collect(context.WithoutCancel(ctx), w.id, spec)

	if res.TimedOut {
		return res, fmt.Errorf("timed out after %ds", spec.TimeoutSeconds)
	}
	if collectErr != nil {
		return res, collectErr
	}
	if runErr != nil && res.ExitCode < 0 {
		return res, fmt.Errorf("could not run in the warm container: %w: %s", runErr, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}

// clearPayload empties the previous run's directory.
//
// Its CONTENTS rather than the directory itself, and as the sandbox user rather
// than as root. Both of those are consequences of the container being properly
// confined, and both were found by getting them wrong:
//
//   - `rm -rf /work` as root failed with "Permission denied", because
//     --cap-drop=ALL takes CAP_DAC_OVERRIDE with it, and a root that cannot
//     override file permissions cannot write into a directory the sandbox user
//     owns. Every container was being retired instead of reused, silently, and
//     two tests passed because of it;
//   - removing /work itself needs write on /, which is root's. Removing what is
//     in it needs write on /work, which the sandbox user has, because the
//     payload was written as theirs.
func (d *Docker) clearPayload(ctx context.Context, id string) error {
	var errOut bytes.Buffer
	// The glob is the shell's, so it has to run in one: three patterns to catch
	// dotfiles as well, and `true` at the end because an empty directory makes
	// every one of them fail to match.
	if err := d.cli(ctx, nil, &errOut, "exec", "--user", "65534:65534", id,
		"sh", "-c", "rm -rf "+workDir+"/* "+workDir+"/.[!.]* "+workDir+"/..?* 2>/dev/null; true"); err != nil {
		return fmt.Errorf("clear the previous run out of a warm container: %w: %s",
			err, strings.TrimSpace(errOut.String()))
	}
	return nil
}

// Close retires every pooled container. A server that exits leaving containers
// running is a server that leaks a machine's memory to whoever looks next.
func (d *Docker) Close() {
	if d == nil || d.pool == nil {
		return
	}
	for _, w := range d.pool.drain() {
		d.remove(context.Background(), w.id)
	}
}
