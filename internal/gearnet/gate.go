package gearnet

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultListen keeps the gate on this machine unless an operator says
// otherwise. On Docker Desktop a container reaches a host loopback service
// through host.docker.internal, so this is enough on a laptop. On Linux, where
// host.docker.internal is the bridge gateway rather than the host's loopback,
// an operator with granted gears sets gear_proxy_listen to that gateway
// address — see config.Config.GearProxyListen.
const DefaultListen = "127.0.0.1:0"

// ListenFor decides where the gate should bind so a sandboxed gear can actually
// reach it, given what the operator configured and what the sandbox is.
//
// The default is right on a laptop and wrong on a server, which is the worst
// combination: Docker Desktop forwards host.docker.internal to the host's
// loopback, so a gate on 127.0.0.1 works on every Mac. On Linux that name is
// the docker bridge gateway, a different address entirely, and every granted
// gear got "connection refused" — the feature was broken on the platform
// servers run on and fine on the platform it was written on. The configuration
// comment told operators to set this by hand, which is documentation standing
// in for a default that works.
//
// An operator who names an address gets it, unchanged and unchecked: a gate
// that cannot bind where it was told is a fault worth failing loudly on.
func ListenFor(ctx context.Context, configured string, box any) string {
	if configured != "" && configured != DefaultListen {
		return configured
	}
	gw, ok := box.(interface {
		HostGatewayAddr(context.Context) string
	})
	if !ok {
		return DefaultListen
	}
	addr := gw.HostGatewayAddr(ctx)
	if addr == "" {
		return DefaultListen
	}
	// Probe rather than assume. On Docker Desktop the daemon answers with the
	// bridge gateway too, but that address lives inside its virtual machine and
	// this host cannot bind it — and there the loopback is what the container
	// reaches anyway. One bind attempt tells the two apart without asking which
	// operating system this is.
	candidate := net.JoinHostPort(addr, "0")
	ln, err := net.Listen("tcp", candidate)
	if err != nil {
		return DefaultListen
	}
	ln.Close()
	return candidate
}

// closeGrace is how long a finished run's credential stays known to the gate.
//
// Not generosity: a gear that backgrounded something outlives its own run, and
// deleting the credential the instant the run ends would make that traffic
// arrive anonymous — answered with a 407 and recorded as belonging to nobody.
// Keeping it briefly means the row names the gear that left the process behind,
// which is the one thing an operator would want to know about it. It is still
// refused; only the record improves.
const closeGrace = 5 * time.Minute

// Gate is the proxy a granted gear reaches the network through, and the writer
// of the log of what it reached. One per server.
type Gate struct {
	store *Store
	ln    net.Listener
	srv   *http.Server
	tr    *http.Transport
	ca    *authority

	mu      sync.Mutex
	tickets map[string]*Ticket
}

// New starts the gate. The listener is opened here rather than lazily on the
// first granted gear because a gate that cannot bind is a configuration fault,
// and the operator should learn about it at startup rather than at the moment a
// pipeline needed it.
func New(db *sql.DB, listen, dir string) (*Gate, error) {
	if listen == "" {
		listen = DefaultListen
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("the gear network gate could not listen on %s: %w — set gear_proxy_listen to an address this machine holds", listen, err)
	}
	g := &Gate{store: NewStore(db), ln: ln, tickets: map[string]*Ticket{}}
	// The signing identity for the one case that needs it: a run holding secret
	// stand-ins, whose TLS this gate has to read inside to put the real value
	// back. Built at startup rather than at first use so a directory it cannot
	// write is a startup failure, not a gear failing later for a reason that
	// has nothing to do with the gear.
	//
	// Not fatal when dir is empty: that is a test or an embedding with no data
	// directory, and it means references are simply never minted.
	if dir != "" {
		ca, err := loadAuthority(dir)
		if err != nil {
			ln.Close()
			return nil, err
		}
		g.ca = ca
	}
	g.tr = &http.Transport{
		DialContext: g.dialContext,
		// This server's own environment is never consulted for a proxy: a gear's
		// traffic must go where the operator's grant says, not wherever the
		// machine that happens to be hosting it points its own HTTP_PROXY.
		Proxy: nil,
		// A pooled connection would be reused across runs, so one gear's row
		// would account for another gear's bytes. Plain-HTTP proxying is the
		// rare path (everything with a scheme of https is a CONNECT tunnel), so
		// the cost of turning reuse off is nothing worth measuring.
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	g.srv = &http.Server{Handler: g, ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := g.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("the gear network gate stopped serving", "err", err)
		}
	}()
	slog.Info("gear network gate listening", "addr", ln.Addr().String(),
		"note", "only a gear the operator granted the network is given the credential for it")
	return g, nil
}

// CACert is the certificate a run must trust when this gate reads inside its
// TLS. The signing key is never exposed — this is the half that lets a gear
// verify the gate, and nothing else.
func (g *Gate) CACert() []byte {
	if g == nil || g.ca == nil {
		return nil
	}
	return g.ca.pem
}

// Store exposes the log, so a caller holding the gate does not also have to be
// handed the store and keep the two in step.
func (g *Gate) Store() *Store { return g.store }

// Addr is where the gate listens, for the log and for the interface.
func (g *Gate) Addr() string { return g.ln.Addr().String() }

func (g *Gate) Close() error { return g.srv.Close() }

// Ticket is one run's permission to use the gate. It exists for the length of
// the run and its credential is worth nothing afterwards.
type Ticket struct {
	gate   *Gate
	token  string
	grant  Grant
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool

	// refs maps a stand-in to the real value it will become on the way out.
	// It lives for exactly as long as the ticket: when the run ends, every
	// stand-in it minted stops meaning anything.
	mu   sync.Mutex
	refs map[string]string
}

// Open registers a run. The returned ticket must be closed when the run ends —
// its credential stops working then, and every connection still open is cut.
func (g *Gate) Open(grant Grant) (*Ticket, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("the gear network gate could not issue a credential for this run: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Ticket{
		gate:  g,
		token: base64.RawURLEncoding.EncodeToString(raw),
		grant: grant, ctx: ctx, cancel: cancel,
	}
	g.mu.Lock()
	g.tickets[t.token] = t
	g.mu.Unlock()
	return t, nil
}

// Close ends the run's access. Connections still open are cut: a gear whose
// process has been killed must not leave a tunnel carrying bytes.
func (t *Ticket) Close() {
	if t == nil || !t.closed.CompareAndSwap(false, true) {
		return
	}
	t.cancel()
	time.AfterFunc(closeGrace, func() {
		t.gate.mu.Lock()
		delete(t.gate.tickets, t.token)
		t.gate.mu.Unlock()
	})
}

// ProxyURL is what the run is told to use. hostname is how the run reaches this
// machine — a container says host.docker.internal, a subprocess says loopback —
// and empty means the address the gate is actually bound to.
//
// The credential travels as the URL's user, which is the form every HTTP client
// that reads HTTP_PROXY already understands.
func (t *Ticket) ProxyURL(hostname string) string {
	host, port, err := net.SplitHostPort(t.gate.Addr())
	if err != nil {
		return ""
	}
	if hostname != "" {
		host = hostname
	} else if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		// A gate bound to 0.0.0.0 has no address of its own to hand out; the
		// loopback is the one that certainly reaches it from this machine.
		host = "127.0.0.1"
	}
	u := url.URL{Scheme: "http", User: url.UserPassword(t.token, "x"), Host: net.JoinHostPort(host, port)}
	return u.String()
}

// Env is the proxy configuration to put in a granted run's environment.
//
// Both cases of each name, because the two conventions are split down the
// middle of the ecosystem — curl and Python read the lowercase ones, Go and
// most JVM clients the uppercase — and a gear whose author picked the other one
// would silently bypass the record.
func (t *Ticket) Env(hostname string) map[string]string {
	u := t.ProxyURL(hostname)
	return map[string]string{
		"HTTP_PROXY":  u,
		"HTTPS_PROXY": u,
		"ALL_PROXY":   u,
		"http_proxy":  u,
		"https_proxy": u,
		"all_proxy":   u,
	}
}

func (t *Ticket) done() bool { return t.closed.Load() }

// ticket identifies the run behind a request. The credential is the proxy
// user; the password is ignored, and exists only because some clients will not
// send a user without one.
func (g *Gate) ticket(r *http.Request) *Ticket {
	raw := r.Header.Get("Proxy-Authorization")
	scheme, encoded, ok := strings.Cut(raw, " ")
	if !ok || !strings.EqualFold(scheme, "basic") {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	user, _, _ := strings.Cut(string(decoded), ":")
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tickets[user]
}

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := g.ticket(r)
	if t == nil {
		// Deliberately not recorded. Anything can reach this port and present
		// nothing; writing a row for each would let a stranger fill the
		// operator's audit log with noise, and a row that cannot name a gear
		// answers no question anybody has.
		w.Header().Set("Proxy-Authenticate", `Basic realm="cogitorium gear network"`)
		http.Error(w, "this is the gear network gate: it carries traffic for a gear the operator granted the network, "+
			"and the credential for that run has to come with the request", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		g.connect(w, r, t)
		return
	}
	g.forward(w, r, t)
}

// attempt is a connection that has been checked and written down, and may now
// happen.
type attempt struct {
	id   int64
	host string
	port int
}

// begin is the whole check, in one place so the tunnel and the plain request
// cannot grow different rules — and it writes the row BEFORE returning, so
// there is no path in this package that opens a socket without a record.
//
// A non-nil error means refused, and the row already says why; the returned
// status and message are what the gear is told.
func (g *Gate) begin(t *Ticket, method, target, defaultPort string) (*attempt, int, string) {
	// Written with the run's cancellation stripped: the row must land even when
	// what ends the connection is the run itself ending.
	ctx := context.WithoutCancel(t.ctx)

	rec := Connection{
		GearName: t.grant.GearName, Version: t.grant.Version,
		WorkspaceID: t.grant.WorkspaceID, AgentID: t.grant.AgentID, AgentName: t.grant.AgentName,
		Method: method, Allowed: t.grant.Hosts,
	}
	if t.grant.GearID != 0 {
		id := t.grant.GearID
		rec.GearID = &id
	}
	if rec.Allowed == nil {
		rec.Allowed = []string{}
	}

	host, port, ok := splitTarget(target, defaultPort)
	rec.Host, rec.Port = host, port

	refuse := func(state, why string, status int) (*attempt, int, string) {
		rec.State, rec.Error = state, why
		if _, err := g.store.Record(ctx, rec); err != nil {
			slog.Error("could not record a refused gear connection", "gear", t.grant.GearName, "err", err)
		}
		slog.Warn("gear connection refused", "gear", t.grant.GearName, "host", host, "port", port,
			"method", method, "state", state)
		return nil, status, why
	}

	switch {
	case !ok:
		return refuse(StateRefusedInvalid,
			fmt.Sprintf("%q is not a host and port this gate can dial", target), http.StatusBadRequest)
	case t.done():
		return refuse(StateRefusedNoGrant,
			"the run this credential belongs to has finished, so nothing may be sent on its behalf", http.StatusProxyAuthRequired)
	case !Allows(t.grant.Hosts, host):
		return refuse(StateRefusedDestination,
			fmt.Sprintf("%s is not among the destinations the operator granted this gear: %s",
				host, strings.Join(t.grant.Hosts, ", ")), http.StatusForbidden)
	}

	rec.State = StateOpen
	id, err := g.store.Record(ctx, rec)
	if err != nil {
		// No row, no connection. The audit trail is the reason this gate exists
		// at all, so a run that cannot be recorded does not proceed.
		slog.Error("could not record a gear connection, so it was not made", "gear", t.grant.GearName, "err", err)
		return nil, http.StatusServiceUnavailable,
			"this connection could not be written to the gear's connection log, so it was not made"
	}
	return &attempt{id: id, host: host, port: port}, 0, ""
}

func (g *Gate) settle(t *Ticket, a *attempt, state string, sent, received int64, cause error) {
	errText := ""
	if cause != nil {
		errText = cause.Error()
	}
	if err := g.store.Settle(context.WithoutCancel(t.ctx), a.id, state, sent, received, errText); err != nil {
		slog.Warn("could not settle a gear connection record", "id", a.id, "err", err)
	}
	slog.Info("gear connection", "gear", t.grant.GearName, "host", a.host, "port", a.port,
		"state", state, "bytes_sent", sent, "bytes_received", received)
}

// connect carries a tunnel, which is every https:// call a gear makes. What
// travels inside it is opaque to this gate on purpose: terminating TLS would
// mean holding a certificate authority a gear trusts, which is a much larger
// thing to own than a host list.
func (g *Gate) connect(w http.ResponseWriter, r *http.Request, t *Ticket) {
	a, status, msg := g.begin(t, http.MethodConnect, r.URL.Host, "443")
	if a == nil {
		http.Error(w, msg, status)
		return
	}

	// A run holding stand-ins cannot be tunnelled: the whole point is to put the
	// real value into the request, and a tunnel is bytes this gate must not be
	// able to read. Only that run, and only while it holds them.
	if t.holdsReferences() && g.ca != nil {
		g.intercept(w, r, t, a)
		return
	}

	upstream, err := g.dial(t.ctx, a.host, a.port)
	if err != nil {
		if errors.Is(err, errLocal) {
			g.settle(t, a, StateRefusedLocal, 0, 0, err)
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		g.settle(t, a, StateFailed, 0, 0, err)
		http.Error(w, "could not reach "+a.host+": "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	client, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		g.settle(t, a, StateFailed, 0, 0, err)
		http.Error(w, "this gate cannot take over the connection: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if _, err := buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		g.settle(t, a, StateFailed, 0, 0, err)
		return
	}
	if err := buf.Flush(); err != nil {
		g.settle(t, a, StateFailed, 0, 0, err)
		return
	}

	sent, received, cerr := splice(t.ctx, client, buf, upstream)
	state := StateClosed
	if cerr != nil {
		state = StateFailed
	}
	g.settle(t, a, state, sent, received, cerr)
}

// forward carries a plain http:// request. Rarer than the tunnel and kept for
// the same reason the tunnel is checked: a gear reaching an internal service
// over plain HTTP is a connection, and a log missing it is a log nobody can
// rely on.
func (g *Gate) forward(w http.ResponseWriter, r *http.Request, t *Ticket) {
	if !r.URL.IsAbs() {
		http.Error(w, "this is the gear network gate, not a web server: send a proxied request", http.StatusBadRequest)
		return
	}
	a, status, msg := g.begin(t, r.Method, r.URL.Host, "80")
	if a == nil {
		http.Error(w, msg, status)
		return
	}

	// The request dies with the run as well as with the client, so a gear that
	// is killed mid-request does not leave one in flight.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(t.ctx, cancel)()

	body := &counter{r: r.Body}
	out := r.Clone(ctx)
	out.RequestURI = ""
	out.Body = io.NopCloser(body)
	stripHopByHop(out.Header)
	// Plain HTTP needs no interception to be readable, so a stand-in is put
	// back here exactly as it is inside a terminated connection.
	t.substitute(out)

	resp, err := g.tr.RoundTrip(out)
	if err != nil {
		if errors.Is(err, errLocal) {
			g.settle(t, a, StateRefusedLocal, body.n.Load(), 0, err)
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		g.settle(t, a, StateFailed, body.n.Load(), 0, err)
		http.Error(w, "could not reach "+a.host+": "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	stripHopByHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	received, cerr := io.Copy(w, resp.Body)
	state := StateClosed
	if cerr != nil {
		state = StateFailed
	}
	g.settle(t, a, state, body.n.Load(), received, cerr)
}

// errLocal is refused regardless of what the operator granted. "The network"
// does not mean this machine: 127.0.0.1 is this server's own API, where a
// loopback request is trusted as the administrator, and 169.254.169.254 is the
// cloud metadata endpoint. Neither is something anybody grants by allowing a
// gear to reach the internet.
var errLocal = errors.New("this address belongs to the machine the server runs on")

// dial resolves and connects, refusing anything that points back here.
//
// Resolution happens once and the surviving address is what gets dialled, so a
// name that answers differently a moment later cannot walk a connection past
// the check that already passed.
func (g *Gate) dial(ctx context.Context, host string, port int) (net.Conn, error) {
	return g.dialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

func (g *Gate) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := routableAddrs(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	var last error
	for _, ip := range addrs {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// routableAddrs turns a host into the addresses that may be dialled, or an
// error naming the refusal.
func routableAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !routable(ip) {
			return nil, fmt.Errorf("%s: %w, so a gear is never allowed to reach it", host, errLocal)
		}
		return []netip.Addr{ip}, nil
	}
	found, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(found))
	for _, ip := range found {
		if routable(ip) {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s resolves only to addresses on the machine the server runs on: %w", host, errLocal)
	}
	return out, nil
}

func routable(ip netip.Addr) bool {
	ip = ip.Unmap()
	return !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsMulticast()
}

// splitTarget parses "host", "host:port" or an IPv6 literal in brackets.
func splitTarget(target, defaultPort string) (string, int, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", 0, false
	}
	host, rawPort, err := net.SplitHostPort(target)
	if err != nil {
		host, rawPort = strings.Trim(target, "[]"), defaultPort
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return strings.ToLower(host), 0, false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return "", port, false
	}
	return host, port, true
}

// splice carries the tunnel in both directions and counts what crossed.
//
// buf rather than the raw connection on the way up: bytes the client pipelined
// straight after CONNECT are already in the hijacked reader, and reading past
// them would start the tunnel one TLS ClientHello short.
func splice(ctx context.Context, client net.Conn, buf *bufio.ReadWriter, upstream net.Conn) (int64, int64, error) {
	// Closing both ends is what makes a cancelled run actually stop: an
	// io.Copy blocked on a read does not watch a context.
	defer context.AfterFunc(ctx, func() {
		client.Close()
		upstream.Close()
	})()

	var sent, received atomic.Int64
	errc := make(chan error, 2)
	go func() {
		n, err := io.Copy(upstream, buf)
		sent.Add(n)
		halfClose(upstream)
		errc <- err
	}()
	go func() {
		n, err := io.Copy(client, upstream)
		received.Add(n)
		halfClose(client)
		errc <- err
	}()

	var first error
	for range 2 {
		if err := <-errc; err != nil && first == nil && !ignorable(err) {
			first = err
		}
	}
	return sent.Load(), received.Load(), first
}

// halfClose tells the far end that this side is finished writing, so a server
// waiting for the request to end does not wait for the whole tunnel to be torn
// down instead.
func halfClose(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

// ignorable separates a tunnel that ended from a tunnel that broke. Both
// directions closing, and the sockets being closed under them when the run
// finishes, are how a normal connection ends.
func ignorable(err error) bool {
	// A reset is how a tunnel ordinarily ends.
	//
	// Once CONNECT has succeeded and bytes have flowed, either side may tear
	// the socket down with an RST rather than a FIN — Go's own http.Transport
	// does it on CloseIdleConnections, and browsers do it constantly. Treating
	// that as a failure filled the connection log, which is an audit trail an
	// operator reads, with failures that did not happen; and a log where
	// ordinary traffic says "failed" is one where a real failure is invisible.
	//
	// ECONNRESET is the read side of that, EPIPE the write side. Neither can
	// reach here from a connection that never established: dial failures are
	// settled before the tunnel starts, a few lines up.
	//
	// Found by CI rather than by reading: this test passed on macOS, where the
	// same teardown usually arrives as a clean FIN, and failed on Linux.
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

// counter reads a body and remembers how much of it there was, because a
// request body's length is not knowable in advance and "how much left" is half
// of what the log is for.
type counter struct {
	r io.Reader
	n atomic.Int64
}

func (c *counter) Read(p []byte) (int, error) {
	if c.r == nil {
		return 0, io.EOF
	}
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// hopByHop headers belong to one connection and must not be forwarded. The
// proxy credential is stripped with them: it is this gate's, and passing it
// upstream would hand a gear's own run token to a stranger's server.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func stripHopByHop(h http.Header) {
	for _, name := range h.Values("Connection") {
		for _, part := range strings.Split(name, ",") {
			if part = strings.TrimSpace(part); part != "" {
				h.Del(part)
			}
		}
	}
	for _, name := range hopByHop {
		h.Del(name)
	}
}
