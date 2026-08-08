package sandbox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Start opens an interactive session — a terminal — inside a container.
// It is the same isolation gears get: the server's filesystem, its database
// and the provider keys in it are not present, so what the operator (or
// anything that reaches this) can touch is the container and nothing else.
func (d *Docker) Start(ctx context.Context, spec Spec) (Session, error) {
	id, err := d.create(ctx, spec, true)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, d.bin, "start", "-a", "-i", id)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		d.remove(context.WithoutCancel(ctx), id)
		return nil, fmt.Errorf("terminal stdin: %w", err)
	}
	// With -t the container's stdout and stderr are the same stream, which
	// is what a terminal is.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.remove(context.WithoutCancel(ctx), id)
		return nil, fmt.Errorf("terminal stdout: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		d.remove(context.WithoutCancel(ctx), id)
		return nil, fmt.Errorf("start terminal: %w", err)
	}
	slog.Info("terminal session opened", "backend", d.Name(), "container", id[:12])

	return &dockerSession{
		docker: d,
		id:     id,
		cmd:    cmd,
		in:     stdin,
		out:    stdout,
	}, nil
}

type dockerSession struct {
	docker *Docker
	id     string
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
}

func (s *dockerSession) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *dockerSession) Write(p []byte) (int, error) { return s.in.Write(p) }

func (s *dockerSession) Close() error {
	s.in.Close()
	s.out.Close()
	// Removing the container is what actually ends the session; killing the
	// client process alone would leave it running.
	s.docker.remove(context.Background(), s.id)
	slog.Info("terminal session closed", "container", s.id[:12])
	return nil
}

func (s *dockerSession) Wait() error { return s.cmd.Wait() }

// Resize tells the container's pseudo-terminal how large the client's view
// is. The CLI has no resize command, so this goes to the Engine API over
// its socket; failing to resize is cosmetic and never ends the session.
func (s *dockerSession) Resize(rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return nil
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", dockerSocket)
			},
		},
		Timeout: 3 * time.Second,
	}
	url := fmt.Sprintf("http://docker/containers/%s/resize?h=%d&w=%d", s.id, rows, cols)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("terminal resize unavailable", "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Debug("terminal resize refused", "status", resp.Status, "body", strings.TrimSpace(string(body)))
	}
	return nil
}

const dockerSocket = "/var/run/docker.sock"

// ShellSpec builds the Spec for an operator terminal: an interactive shell
// in a scratch container with no network and no host files.
func ShellSpec(rows, cols uint16) Spec {
	return Spec{
		Command: "/bin/sh",
		Args:    []string{"-i"},
		Env: map[string]string{
			"HOME":    "/tmp",
			"TERM":    "xterm-256color",
			"PS1":     "cogitorium:\\w$ ",
			"LINES":   strconv.Itoa(int(rows)),
			"COLUMNS": strconv.Itoa(int(cols)),
		},
		Writable: true,
	}
}
