// Package imagert runs a plugin's backend as one container per invocation.
//
// This is the isolated tier, and it is the answer to the honest limitation of
// the provisioned one: a worker is a supervised child of the server, running
// as the server's user with the server's files. A plugin that needs isolation
// gets this instead — cold and isolated rather than warm and not, which is a
// trade an author reads and chooses rather than one the host makes for them.
//
// It is built on the sandbox this server already has, and adds no isolation
// mechanism of its own. No new interface, no new RBAC verb, no Docker socket
// mounted anywhere: a run is a Spec handed to whichever backend the operator
// configured, exactly as a gear's run is. The cost is a container start per
// request, and that is stated up front rather than optimised away quietly
// later.
package imagert

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/abi"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
)

// Spec is how to run one plugin's image.
type Spec struct {
	Plugin string
	// Image is the digest-pinned reference the manifest named. Pinned because
	// a moving tag would change what an operator approved without the
	// approval changing — the same rule the gear catalog already lives under.
	Image string
	// Dir is the plugin's bundle on the host. The sandbox copies it in; it is
	// never bind-mounted, which is what lets this work against a remote or
	// rootless daemon.
	Dir string
	// Command and Args are the entrypoint inside the image.
	Command string
	Args    []string
	// Env carries no credential. A secret reaches a plugin as a stand-in the
	// gate substitutes at the edge, exactly as it does for a gear.
	Env map[string]string
	// Network is whether this plugin was granted any outbound reach at all.
	// The gate decides destinations; this decides whether there is a gate to
	// talk to.
	Network bool
	// Timeout bounds one invocation.
	Timeout time.Duration
}

// Runner invokes a plugin's image.
type Runner struct {
	sb sandbox.Runner
}

// New returns a runner, or nil when this install has no backend that can start
// a container.
//
// Availability follows the LIVE backend, never the channel's name: the shipped
// compose image is itself a container and still cannot start one, while a
// native install with Docker can.
func New(sb sandbox.Runner) *Runner {
	if sb == nil {
		return nil
	}
	return &Runner{sb: sb}
}

// Available reports whether this tier can run here.
func (r *Runner) Available() bool { return r != nil && r.sb != nil }

// Backend names the sandbox in use, for a screen that says what an operator
// actually has.
func (r *Runner) Backend() string {
	if !r.Available() {
		return ""
	}
	return r.sb.Name()
}

// Call runs one invocation.
//
// The conversation is the same envelope every other tier carries: the request
// on stdin, the response on stdout. Nothing about the tier is visible in it,
// which is what lets a plugin move between tiers without its author changing a
// line.
func (r *Runner) Call(ctx context.Context, spec Spec, req abi.Request) (abi.Response, error) {
	if !r.Available() {
		return abi.Response{}, fmt.Errorf("plugin %q runs in a container image and this install "+
			"has no sandbox backend that can start one", spec.Plugin)
	}

	req.Contract = abi.Version
	in, err := json.Marshal(req)
	if err != nil {
		return abi.Response{}, err
	}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	res, err := r.sb.Run(ctx, sandbox.Spec{
		Dir:            spec.Dir,
		Command:        spec.Command,
		Args:           spec.Args,
		Stdin:          strings.NewReader(string(in)),
		Env:            spec.Env,
		TimeoutSeconds: int(timeout / time.Second),
		Network:        spec.Network,
		Image:          spec.Image,
		// Never reusable. A warm container shares /tmp with whatever ran in it
		// before, and a plugin invocation is exactly the kind of run that may
		// have been handed a credential stand-in — the same reasoning the gear
		// executor already applies.
		Reusable: false,
		// The payload is the plugin's to read and run, and nothing more.
		Writable: false,
	})
	if err != nil {
		return abi.Response{}, fmt.Errorf("plugin %q: %w%s", spec.Plugin, err, tail(res.Stderr))
	}
	if res.TimedOut {
		return abi.Response{}, fmt.Errorf("plugin %q did not finish within %s and was stopped%s",
			spec.Plugin, timeout, tail(res.Stderr))
	}
	if res.ExitCode != 0 {
		// The container's own last words are the diagnosis. Without them this
		// is "exit status 1", which tells an operator nothing they can act on.
		return abi.Response{}, fmt.Errorf("plugin %q exited %d%s",
			spec.Plugin, res.ExitCode, tail(res.Stderr))
	}

	var resp abi.Response
	if err := json.Unmarshal([]byte(res.Stdout), &resp); err != nil {
		return abi.Response{}, fmt.Errorf("plugin %q wrote something to stdout that is not a "+
			"response envelope: %w%s", spec.Plugin, err, tail(res.Stderr))
	}
	if err := resp.Validate(); err != nil {
		return abi.Response{}, fmt.Errorf("plugin %q: %w", spec.Plugin, err)
	}
	return resp, nil
}

// tail appends the container's stderr, bounded. A plugin that writes a
// megabyte of warnings should not turn one failure into a megabyte of log.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	const keep = 4 << 10
	if len(s) > keep {
		s = "…" + s[len(s)-keep:]
	}
	return ". Its last output was:\n" + s
}
