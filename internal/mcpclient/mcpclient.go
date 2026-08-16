// Package mcpclient speaks the Model Context Protocol to somebody else's
// server, so an agent can be granted its tools the way it is granted a gear.
//
// # What this runs, and where
//
// An external MCP server is a command. Cogitorium never sees its source, cannot
// version it, and cannot check it against anything — the "tool list" it reports
// is the server's own account of itself. In this first cut the child runs on the
// host, as this server's own user, OUTSIDE the sandbox. It therefore has the
// server's file access, which includes the SQLite database and every provider
// key in it.
//
// That is the exact attack internal/sandbox exists to prevent, and it is stated
// here rather than in a release note. What bounds it is policy rather than
// isolation: the capability is off unless configured on, every write is
// admin-only with no agent-reachable path, each tool is approved individually,
// and the command is fingerprinted at approval and re-checked at every spawn.
// Read internal/mcpstore for what the operator is actually agreeing to.
//
// # The protocol hazards this file exists to get right
//
// A pipe carries three kinds of line and they cannot be told apart by position:
// the answer to something we asked, a NOTIFICATION (no id — answering one is a
// protocol error, not a harmless extra), and a REQUEST from the server, which
// expects an answer from us. A client that assumes the next line is its own
// answer deadlocks the first time a server sends anything else, and MCP servers
// send notifications routinely.
package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
	"github.com/orkcom-tech/cogitorium/internal/procgroup"
)

// The version this client states. The server answers with what it will use.
const protocolVersion = "2024-11-05"

// stderrKept is how much of the child's stderr is held for the error message.
//
// A server that dies on startup says why on stderr and nowhere else. Without
// this the operator gets "exit status 1", which is true and useless.
const stderrKept = 4096

// Spec is what to run, and under what bounds.
type Spec struct {
	Name    string
	Command string
	Args    []string
	Dir     string
	// Env is the complete environment. This server's own is never inherited —
	// it holds credentials, and the whole point of naming values is that the
	// child gets what it was granted and nothing else.
	Env map[string]string
	// Timeout bounds one call, not the connection: a slow tool must not take
	// the process down and make the next call pay to start it again.
	Timeout time.Duration
}

// Conn is one running MCP server.
type Conn struct {
	spec Spec
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan mcpwire.Message
	// dead is closed when the reader stops, whatever the reason. Every waiter
	// selects on it, so a child that dies fails its calls instead of leaving
	// them to time out one by one.
	dead     chan struct{}
	deadErr  error
	closed   bool
	stderr   *tailBuffer
	release  func()
	end      context.CancelFunc
	serverIn string // what the server called itself, for the log
}

// Dial spawns the server and completes the handshake.
//
// The handshake is not optional politeness: a server may not answer tools/list
// before initialize, and one that does is not one to rely on.
func Dial(ctx context.Context, spec Spec) (*Conn, error) {
	if spec.Timeout <= 0 {
		spec.Timeout = 60 * time.Second
	}
	// The child gets the CONNECTION's own context, not the caller's. Binding it
	// to the request that happened to open it would kill the server the moment
	// that request ended, which is the opposite of a connection. Cancelling this
	// one is what Close does, and procgroup.Isolate hangs the group kill off it.
	life, end := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(life, spec.Command, spec.Args...)
	// The child is given time to leave after stdin closes before the group is
	// killed; without this, cancelling races the ordinary shutdown.
	cmd.WaitDelay = 3 * time.Second
	cmd.Dir = spec.Dir
	cmd.Env = envList(spec.Env)
	// The child's own group, so closing takes whatever it started with it. An
	// MCP server run through npx is a wrapper that execs the real one.
	afterStart, release := procgroup.Isolate(cmd)

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		release()
		end()
		return nil, fmt.Errorf("start the MCP server %q: %w — the command is %s", spec.Name, err, spec.Command)
	}
	afterStart()

	c := &Conn{
		spec: spec, cmd: cmd, in: in, out: bufio.NewReader(out),
		pending: map[string]chan mcpwire.Message{},
		dead:    make(chan struct{}),
		stderr:  &tailBuffer{limit: stderrKept},
		release: release,
		end:     end,
	}
	go io.Copy(c.stderr, errPipe)
	go c.read()

	if err := c.handshake(ctx); err != nil {
		c.Close()
		return nil, err
	}
	slog.Warn("an external MCP server was started on this host, outside the sandbox",
		"server", spec.Name, "command", spec.Command, "identified_as", c.serverIn,
		"note", "it runs as this server's user and has this server's file access")
	return c, nil
}

func (c *Conn) handshake(ctx context.Context) error {
	res, err := c.Call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "cogitorium", "version": "1"},
	})
	if err != nil {
		return fmt.Errorf("the MCP server %q would not initialize: %w", c.spec.Name, err)
	}
	var info struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if json.Unmarshal(res, &info) == nil {
		c.serverIn = strings.TrimSpace(info.ServerInfo.Name + " " + info.ServerInfo.Version)
	}
	// The spec requires this, and a server that is waiting for it will answer
	// nothing until it arrives. It is a notification, so there is no reply to
	// wait for.
	return c.notify("notifications/initialized", map[string]any{})
}

// Tools asks what the server offers, following cursors to the end.
//
// Paginated because a server with more than a page of tools is ordinary, and a
// client that reads the first page silently offers a subset. Capped because
// three hundred tool definitions in every request is a bill rather than a
// capability, and the caller is told what was dropped.
func (c *Conn) Tools(ctx context.Context, cap int) ([]mcpwire.Tool, bool, error) {
	var all []mcpwire.Tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.Call(ctx, "tools/list", params)
		if err != nil {
			return nil, false, err
		}
		var page struct {
			Tools      []mcpwire.Tool `json:"tools"`
			NextCursor string         `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, false, fmt.Errorf("the MCP server %q answered tools/list with something this "+
				"cannot read: %w", c.spec.Name, err)
		}
		all = append(all, page.Tools...)
		if len(all) >= cap {
			return all[:cap], true, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return all, false, nil
		}
		cursor = page.NextCursor
	}
}

// CallResult is what a tool returned, flattened to text.
type CallResult struct {
	Text string
	// IsError is the server saying the tool failed on its own terms, which is
	// not the same as the call failing. The model is told either way; only one
	// of them is worth an operator's attention.
	IsError bool
	// Dropped names content this cut does not carry — an image, an audio clip,
	// an embedded resource. Named rather than skipped, because half an answer
	// returned as the whole answer is the failure worth avoiding here.
	Dropped []string
}

// CallTool runs one tool.
func (c *Conn) CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	raw, err := c.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallResult{}, err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return CallResult{}, fmt.Errorf("the MCP server %q answered %q with something this cannot read: %w",
			c.spec.Name, name, err)
	}
	res := CallResult{IsError: out.IsError}
	var text []string
	for _, part := range out.Content {
		if part.Type == "text" {
			text = append(text, part.Text)
			continue
		}
		res.Dropped = append(res.Dropped, part.Type)
	}
	res.Text = strings.Join(text, "\n")
	return res, nil
}

// Call sends a request and waits for its answer.
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	enc, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("this MCP connection is closed")
	}
	c.nextID++
	id := fmt.Sprintf("%d", c.nextID)
	reply := make(chan mcpwire.Message, 1)
	c.pending[id] = reply
	c.mu.Unlock()

	if err := c.write(mcpwire.Message{
		JSONRPC: "2.0", ID: json.RawMessage(`"` + id + `"`), Method: method, Params: enc,
	}); err != nil {
		c.forget(id)
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, c.spec.Timeout)
	defer cancel()

	select {
	case msg := <-reply:
		if msg.Error != nil {
			return nil, fmt.Errorf("the MCP server %q refused %s: %w", c.spec.Name, method, msg.Error)
		}
		return msg.Result, nil
	case <-c.dead:
		return nil, c.death()
	case <-callCtx.Done():
		c.forget(id)
		// The connection survives a call that timed out, so the server is told
		// to stop rather than left producing an answer nobody will read. It is
		// a notification: there is nothing to wait for, and waiting is what
		// just failed.
		_ = c.notify("notifications/cancelled", map[string]any{
			"requestId": id, "reason": "the caller stopped waiting",
		})
		return nil, fmt.Errorf("the MCP server %q did not answer %s within %s",
			c.spec.Name, method, c.spec.Timeout)
	}
}

func (c *Conn) notify(method string, params any) error {
	enc, err := json.Marshal(params)
	if err != nil {
		return err
	}
	// No id: that is the whole of what makes it a notification.
	return c.write(mcpwire.Message{JSONRPC: "2.0", Method: method, Params: enc})
}

func (c *Conn) write(m mcpwire.Message) error {
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.in.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write to the MCP server %q: %w", c.spec.Name, err)
	}
	return nil
}

// read is the only reader of the pipe, and it classifies every line before
// doing anything with it.
func (c *Conn) read() {
	defer close(c.dead)
	for {
		line, err := c.out.ReadBytes('\n')
		if len(line) > 0 {
			c.handle(line)
		}
		if err != nil {
			c.mu.Lock()
			c.deadErr = err
			c.pending = map[string]chan mcpwire.Message{}
			c.mu.Unlock()
			// The waiters are released by close(c.dead) in the defer, and
			// deliberately NOT by closing their own channels: a closed reply
			// channel hands the caller a zero-valued message, which reads as an
			// answer that could not be parsed rather than as a server that
			// died. That is what it did, and the error said "something this
			// cannot read" while quoting nothing.
			return
		}
	}
}

func (c *Conn) handle(line []byte) {
	var m mcpwire.Message
	if err := json.Unmarshal(line, &m); err != nil {
		// Not fatal. Servers print to stdout by mistake, and one stray line is
		// not a reason to take down a working connection.
		slog.Debug("an MCP server sent a line that is not JSON-RPC", "server", c.spec.Name)
		return
	}
	switch {
	case m.IsResponse():
		c.deliver(m)
	case m.IsNotification():
		// Nothing is written back. Answering a notification is a protocol
		// error, and tools/list_changed is the one servers send most.
		slog.Debug("notification from an MCP server", "server", c.spec.Name, "method", m.Method)
	case m.IsRequest():
		c.refuse(m)
	}
}

// refuse answers a request from the server.
//
// Every one of them, for now, and deliberately: sampling/createMessage asks
// this install to spend its own model budget on text the server chose, and
// elicitation/create asks a human who is not there on an unattended run.
// Answering "not implemented" is the protocol's own way to say so; silence
// would leave the server waiting forever.
func (c *Conn) refuse(m mcpwire.Message) {
	_ = c.write(mcpwire.Message{
		JSONRPC: "2.0", ID: m.ID,
		Error: &mcpwire.RPCError{
			Code:    mcpwire.CodeMethodNotFound,
			Message: "this client does not implement " + m.Method,
		},
	})
}

func (c *Conn) deliver(m mcpwire.Message) {
	var id string
	if err := json.Unmarshal(m.ID, &id); err != nil {
		// A numeric id, which is legal. Compare on the raw text.
		id = strings.Trim(string(m.ID), `"`)
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		slog.Debug("an MCP server answered something nobody asked", "server", c.spec.Name, "id", id)
		return
	}
	ch <- m
}

func (c *Conn) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// death explains a child that stopped, with the last thing it said.
func (c *Conn) death() error {
	c.mu.Lock()
	err := c.deadErr
	c.mu.Unlock()
	last := c.stderr.lastLine()
	switch {
	case last != "" && err != nil:
		return fmt.Errorf("the MCP server %q stopped (%v); it last said: %s", c.spec.Name, err, last)
	case last != "":
		return fmt.Errorf("the MCP server %q stopped; it last said: %s", c.spec.Name, last)
	default:
		return fmt.Errorf("the MCP server %q stopped: %v", c.spec.Name, err)
	}
}

// Close ends the connection and the process group behind it.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	// Closing stdin is how an MCP server is asked to stop. Cancelling the
	// connection's context is what happens to one that does not: procgroup
	// turned that into a kill of the whole group, so a wrapper that exec'd the
	// real server does not leave it behind.
	_ = c.in.Close()
	select {
	case <-c.dead:
	case <-time.After(3 * time.Second):
	}
	c.end()
	_ = c.cmd.Wait()
	c.release()
	return nil
}

// Alive reports whether the child is still there, so a pool can drop a
// connection instead of handing out one that will fail on first use.
func (c *Conn) Alive() bool {
	select {
	case <-c.dead:
		return false
	default:
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.closed
	}
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// tailBuffer keeps the last N bytes written to it, so a child that failed can
// be quoted without holding everything a chatty one ever printed.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimRight(string(t.buf), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}
