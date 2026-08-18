package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
)

// The two transports that reach somebody else's host.
//
// # Why these are not "stdio over a socket"
//
// On stdio there is one duplex stream and a reply is found by matching its id
// against a table. Streamable HTTP has no standing stream at all: EVERY message
// is its own POST, and the answer to that message comes back on that POST's
// response — either as one JSON object, or as an SSE stream that carries
// progress notifications and then the response. So a reply is located by the
// request it arrived on, and the id table exists only because the layer above
// is written once for every transport.
//
// The deprecated 2024-11-05 shape is the other way round and is why both are in
// this file: a GET opens a stream that carries EVERYTHING the server says, and
// the first event on it names a second URL to POST to. That one does look like
// stdio, with a reader goroutine and a separate write path.
//
// # What an operator is actually agreeing to, and it is not the same risk
//
// A stdio server puts somebody else's code on this machine. A remote one does
// not — there is no process, no file access, nothing to sandbox — and that is
// genuinely safer. What it does instead is send a credential and a stream of
// the agent's words to a host somebody else controls, and a URL can be
// repointed without looking any different. Which is why the fingerprint covers
// the URL, and why approving one is still an administrator's act.
const (
	// sseFieldData and sseFieldEvent are the only two SSE fields this client
	// reads. A line starting with a colon is a comment — servers send them as
	// keep-alives on long streams — and must be ignored rather than treated as
	// malformed.
	sseFieldData  = "data:"
	sseFieldEvent = "event:"
)

// dialRemote connects to an MCP server over HTTP and completes the handshake.
func dialRemote(ctx context.Context, spec Spec) (*Conn, error) {
	endpoint, err := url.Parse(spec.URL)
	if err != nil {
		return nil, fmt.Errorf("the MCP server %q has an unusable URL: %w", spec.Name, err)
	}
	if endpoint.Scheme != "https" && endpoint.Hostname() != "localhost" && endpoint.Hostname() != "127.0.0.1" {
		// Plain HTTP to anywhere but this machine would send the credential and
		// everything the agent says in clear. Refused rather than warned about:
		// there is no version of this that is the operator's informed choice,
		// because the thing leaking is not theirs to spend.
		return nil, fmt.Errorf("the MCP server %q is %s; a remote MCP server must be https, "+
			"because its headers carry a credential and its body carries what your agents say",
			spec.Name, spec.URL)
	}

	life, end := context.WithCancel(context.WithoutCancel(ctx))
	be := &remoteBackend{
		name: spec.Name, kind: spec.Transport, url: spec.URL,
		headers: spec.Headers, timeout: spec.Timeout,
		life: life, end: end,
		client: &http.Client{
			// No global timeout: an SSE response stream is meant to stay open,
			// and a client timeout would cut a long tool call off mid-answer.
			// One call is bounded by Conn.Call's own context instead.
			Timeout: 0,
		},
	}
	c := &Conn{
		spec:    spec,
		be:      be,
		pending: map[string]chan mcpwire.Message{},
		mirrors: map[string][]paramHeader{},
		dead:    make(chan struct{}),
	}
	be.conn = c

	if spec.Transport == TransportSSE {
		// The legacy shape has to learn where to POST before it can say
		// anything at all, and that arrives on the stream.
		if err := be.openLegacyStream(ctx); err != nil {
			end()
			return nil, err
		}
	}

	if err := c.handshake(ctx); err != nil {
		c.Close()
		return nil, err
	}
	slog.Info("an external MCP server was reached over HTTP",
		"server", spec.Name, "transport", spec.Transport, "url", spec.URL,
		"identified_as", c.serverIn,
		"note", "no process runs on this host for it; its headers carry whatever credential it was granted")
	return c, nil
}

type remoteBackend struct {
	name    string
	kind    string
	url     string
	headers map[string]string
	timeout time.Duration
	conn    *Conn
	client  *http.Client

	life context.Context
	end  context.CancelFunc

	mu sync.Mutex
	// session is the id a server assigned at initialize. The current revision
	// of the transport has no sessions at all and ignores this; the revisions
	// that most deployed servers speak require it to be echoed on every
	// subsequent request, and omitting it gets a 404 that reads like a wrong
	// URL. Sending one to a server that does not use it is harmless.
	session string
	// postTo is where the legacy transport was told to send. Empty on
	// streamable HTTP, where messages go to the endpoint itself.
	postTo string

	closeOnce sync.Once
	inFlight  sync.WaitGroup
}

func (b *remoteBackend) target() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.postTo != "" {
		return b.postTo
	}
	return b.url
}

// send POSTs one message and feeds whatever comes back into the connection.
//
// It returns as soon as the request is on its way. THE POST IS NOT AWAITED
// HERE, and that is deliberate: Conn.Call writes and then waits on its reply
// channel under its own timeout, so blocking this call until the answer arrived
// would put the response outside the timeout that is supposed to bound it, and
// would serialise calls that the transport allows to overlap.
func (b *remoteBackend) send(m mcpwire.Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	select {
	case <-b.life.Done():
		return errors.New("this MCP connection is closed")
	default:
	}
	b.inFlight.Add(1)
	go func() {
		defer b.inFlight.Done()
		if err := b.post(m, body); err != nil {
			// The failure belongs to the message that caused it, so the caller
			// waiting on that id is answered rather than left to time out. A
			// notification has no id and nobody waiting, so it is logged.
			if len(m.ID) == 0 {
				slog.Warn("an MCP notification could not be delivered", "server", b.name, "method", m.Method, "err", err)
				return
			}
			b.conn.deliver(mcpwire.Message{
				JSONRPC: "2.0", ID: m.ID,
				Error: &mcpwire.RPCError{Code: mcpwire.CodeInternalError, Message: err.Error()},
			})
		}
	}()
	return nil
}

func (b *remoteBackend) post(m mcpwire.Message, body []byte) error {
	req, err := http.NewRequestWithContext(b.life, http.MethodPost, b.target(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Both, and it is required: the server chooses per request whether to
	// answer with a JSON object or with a stream, and a client that accepted
	// only one of them is a client that fails on half the servers.
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if m.Method != "" {
		// Mirrored so intermediaries can route without parsing the body. The
		// spec requires it, and a gateway in front of the server may reject the
		// request without it.
		req.Header.Set("Mcp-Method", m.Method)
	}
	// And the NAME the method acts on, which is required for exactly these
	// three and forbidden to be wrong: a server that validates headers against
	// the body answers 400 with -32020 when it is missing.
	if name := subjectOf(m); name != "" {
		req.Header.Set("Mcp-Name", headerSafe(name))
	}
	// Whatever this call's tool asked to have mirrored. Only ever set around a
	// tools/call, and empty for almost every server.
	if m.Method == "tools/call" {
		for k, v := range b.conn.takeParamHeaders() {
			req.Header.Set(k, v)
		}
	}
	b.apply(req)

	res, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching %s: %w", b.url, err)
	}
	defer res.Body.Close()

	// A session assigned at initialize has to come back on everything after it.
	if id := res.Header.Get("Mcp-Session-Id"); id != "" {
		b.mu.Lock()
		b.session = id
		b.mu.Unlock()
	}

	switch {
	case res.StatusCode == http.StatusAccepted:
		// A notification the server took. There is no body and nothing waiting.
		return nil
	case res.StatusCode >= 400:
		// The body may be a JSON-RPC error, which is more useful than the
		// status. Bounded, because this is somebody else's host.
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if msg, ok := oneMessage(snippet); ok && msg.Error != nil {
			b.conn.handle(snippet)
			return nil
		}
		return fmt.Errorf("%s answered %s: %s", b.url, res.Status, strings.TrimSpace(string(snippet)))
	}

	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		// Everything on this stream belongs to the request that opened it:
		// progress notifications, then the response, which ends it.
		return b.drain(res.Body)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return fmt.Errorf("reading the answer from %s: %w", b.url, err)
	}
	b.conn.handle(raw)
	return nil
}

// maxBody bounds a single non-streamed answer. A tool result is text; a
// hundred megabytes of it is a bill and a memory spike, not an answer.
const maxBody = 8 << 20

// apply adds the headers this server was granted.
//
// The values arrived already resolved — the store holds NAMES and the caller
// turned them into values at connect time, exactly as a child's environment is
// resolved. Nothing here reads a secret store, so nothing here can leak one
// into a log line.
func (b *remoteBackend) apply(req *http.Request) {
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}
	b.mu.Lock()
	session := b.session
	b.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
}

// drain reads an SSE stream, handing every complete event's data to the
// connection.
func (b *remoteBackend) drain(body io.Reader) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), maxBody)
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		joined := strings.Join(data, "\n")
		data = data[:0]
		if strings.TrimSpace(joined) != "" {
			b.conn.handle([]byte(joined))
		}
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			// A blank line ends an event. Everything gathered so far is one
			// message.
			flush()
		case strings.HasPrefix(line, ":"):
			// A comment, which is what a keep-alive is. Ignored rather than
			// parsed — treating it as malformed would kill a long stream on the
			// first quiet minute.
		case strings.HasPrefix(line, sseFieldData):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, sseFieldData), " "))
		case strings.HasPrefix(line, sseFieldEvent):
			// The only event name this client acts on is the legacy transport's
			// `endpoint`, handled where that stream is read.
		}
	}
	flush()
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("the stream from %s ended badly: %w", b.url, err)
	}
	return nil
}

// openLegacyStream implements the deprecated 2024-11-05 transport: a GET that
// stays open and carries everything the server says, whose first event names
// the URL to POST to.
func (b *remoteBackend) openLegacyStream(ctx context.Context) error {
	req, err := http.NewRequestWithContext(b.life, http.MethodGet, b.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	b.apply(req)

	res, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("opening the stream from %s: %w", b.url, err)
	}
	if res.StatusCode >= 400 {
		res.Body.Close()
		return fmt.Errorf("%s answered %s when asked for its stream", b.url, res.Status)
	}

	// The POST URL arrives on the stream, so the caller has to wait for it
	// before anything can be said at all.
	ready := make(chan error, 1)
	go b.readLegacy(res.Body, ready)

	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		res.Body.Close()
		return fmt.Errorf("%s did not name its endpoint before the caller gave up", b.url)
	case <-time.After(30 * time.Second):
		res.Body.Close()
		return fmt.Errorf("%s opened a stream but never named an endpoint to post to", b.url)
	}
}

// readLegacy owns the standing stream for the life of the connection.
func (b *remoteBackend) readLegacy(body io.ReadCloser, ready chan<- error) {
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), maxBody)

	event, named := "", false
	var data []string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			payload := strings.Join(data, "\n")
			data = data[:0]
			if event == "endpoint" {
				// It may be relative to the stream's own URL, and usually is.
				where, err := b.resolve(strings.TrimSpace(payload))
				if err != nil && !named {
					ready <- err
					return
				}
				b.mu.Lock()
				b.postTo = where
				b.mu.Unlock()
				if !named {
					named = true
					ready <- nil
				}
			} else if strings.TrimSpace(payload) != "" {
				b.conn.handle([]byte(payload))
			}
			event = ""
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, sseFieldEvent):
			event = strings.TrimSpace(strings.TrimPrefix(line, sseFieldEvent))
		case strings.HasPrefix(line, sseFieldData):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, sseFieldData), " "))
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	if !named {
		ready <- fmt.Errorf("the stream from %s ended before it named an endpoint: %w", b.url, err)
		return
	}
	// The standing stream IS the connection here, so losing it is the
	// connection dying, exactly as a closed pipe is on stdio.
	b.conn.die(err)
}

func (b *remoteBackend) resolve(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%s named an empty endpoint", b.url)
	}
	base, err := url.Parse(b.url)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s named an unusable endpoint %q: %w", b.url, raw, err)
	}
	abs := base.ResolveReference(ref)
	// A server that names another host is a server redirecting this client's
	// credential somewhere the operator never approved. The fingerprint covers
	// the URL precisely so that cannot happen quietly; this is the same rule at
	// the other end.
	if abs.Host != base.Host {
		return "", fmt.Errorf("%s named an endpoint on %s, which is not where it was approved to be", b.url, abs.Host)
	}
	return abs.String(), nil
}

func (b *remoteBackend) explain(err error) string {
	if err == nil {
		return "the connection to " + b.url + " ended"
	}
	return fmt.Sprintf("%v (%s)", err, b.url)
}

func (b *remoteBackend) close() {
	b.closeOnce.Do(func() {
		// Tell the server the session is over, when there is one. Best effort
		// and short: the newest revision answers 405 because it has no sessions
		// at all, which is not a failure and not worth a log line.
		b.mu.Lock()
		session := b.session
		b.mu.Unlock()
		if session != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.url, nil); err == nil {
				req.Header.Set("Mcp-Session-Id", session)
				for k, v := range b.headers {
					req.Header.Set(k, v)
				}
				if res, err := b.client.Do(req); err == nil {
					res.Body.Close()
				}
			}
			cancel()
		}
		b.end()
		b.inFlight.Wait()
		b.conn.die(errors.New("closed"))
	})
}

// oneMessage parses a body that may or may not be a JSON-RPC message.
func oneMessage(raw []byte) (mcpwire.Message, bool) {
	var m mcpwire.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return mcpwire.Message{}, false
	}
	return m, m.JSONRPC != ""
}

// subjectOf is the value the Mcp-Name header mirrors: the tool, prompt or
// resource a request names.
//
// Only these three methods carry one. Sending the header on anything else is
// not merely useless — a server that validates headers against the body has to
// reject a header whose source field is not there.
func subjectOf(m mcpwire.Message) string {
	switch m.Method {
	case "tools/call", "prompts/get":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &p)
		return p.Name
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(m.Params, &p)
		return p.URI
	}
	return ""
}

// headerSafe encodes a value that cannot travel as a plain HTTP header field.
//
// Header values are visible ASCII, space and tab. A tool name is only
// SHOULD-constrained to that set and a resource URI is not constrained at all,
// so anything outside it — or anything that would be mistaken for the sentinel
// itself — is carried base64-encoded in the format the spec defines, which
// servers decode before comparing against the body.
func headerSafe(v string) string {
	if plainASCII(v) && !strings.HasPrefix(v, sentinelOpen) {
		return v
	}
	return sentinelOpen + base64.StdEncoding.EncodeToString([]byte(v)) + sentinelClose
}

// The markers are case-sensitive and must appear exactly like this.
const (
	sentinelOpen  = "=?base64?"
	sentinelClose = "?="
)

func plainASCII(v string) bool {
	if v != strings.TrimSpace(v) {
		// Leading or trailing whitespace is stripped by intermediaries, so a
		// value carrying it would stop matching the body.
		return false
	}
	for _, r := range v {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}
