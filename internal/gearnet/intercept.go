package gearnet

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// intercept terminates a granted run's TLS so the gate can put the real secret
// where the stand-in is.
//
// This replaces the tunnel for one case and one case only: a run that holds
// references. Everything else keeps the CONNECT it always had, opaque and
// spliced byte for byte, because reading inside a connection that has nothing
// to substitute would be a cost with no purchase.
//
// The gear trusts this gate because the executor handed it the gate's
// certificate; upstream verification is unchanged and real — the gate opens its
// own properly verified TLS connection to the destination, so a bad certificate
// out there still fails, and fails to the gear as a refusal rather than
// silently.
func (g *Gate) intercept(w http.ResponseWriter, r *http.Request, t *Ticket, a *attempt) {
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

	leaf, err := g.ca.leaf(a.host)
	if err != nil {
		g.settle(t, a, StateFailed, 0, 0, err)
		return
	}
	inner := tls.Server(&bufConn{Conn: client, r: buf.Reader}, &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		MinVersion:   tls.VersionTLS12,
	})
	if err := inner.HandshakeContext(t.ctx); err != nil {
		// Nearly always a client that pins certificates or was not given the
		// gate's own. Said plainly in the record rather than left as a closed
		// connection nobody can explain.
		g.settle(t, a, StateFailed, 0, 0, fmt.Errorf("the run holds secret references, so this gate had to read "+
			"inside its TLS, and the client would not accept the gate's certificate: %w", err))
		return
	}
	defer inner.Close()

	var sent, received int64
	reader := bufio.NewReader(inner)
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			if !ignorable(err) && !errors.Is(err, io.EOF) {
				g.settle(t, a, StateFailed, sent, received, err)
				return
			}
			break
		}
		out, up, down, err := g.relay(inner, req, t, a)
		sent += up
		received += down
		if err != nil {
			g.settle(t, a, StateFailed, sent, received, err)
			return
		}
		if !out {
			break
		}
	}
	g.settle(t, a, StateClosed, sent, received, nil)
}

// relay carries one request from inside the terminated connection to the real
// destination and the answer back. It reports whether the connection may carry
// another.
func (g *Gate) relay(client net.Conn, req *http.Request, t *Ticket, a *attempt) (bool, int64, int64, error) {
	req.URL.Scheme = "https"
	req.URL.Host = a.host
	if a.port != 443 {
		req.URL.Host = net.JoinHostPort(a.host, strconv.Itoa(a.port))
	}
	req.RequestURI = ""
	body := &counter{r: req.Body}
	req.Body = io.NopCloser(body)
	stripHopByHop(req.Header)

	// The whole reason this connection was opened rather than tunnelled.
	t.substitute(req)

	resp, err := g.tr.RoundTrip(req.WithContext(t.ctx))
	if err != nil {
		// Answered inside the connection so the gear sees an HTTP failure it
		// can read, rather than a socket that closed for no stated reason.
		writeInner(client, http.StatusBadGateway, "could not reach "+a.host+": "+err.Error())
		return false, body.n.Load(), 0, nil
	}
	defer resp.Body.Close()
	stripHopByHop(resp.Header)

	// The response is NOT rewritten. A stand-in only ever travels outward; if a
	// real secret came back in an answer it would be the destination's to send,
	// and quietly editing somebody's response is not this gate's business.
	down := &counter{r: resp.Body}
	resp.Body = io.NopCloser(down)
	if err := resp.Write(client); err != nil {
		return false, body.n.Load(), down.n.Load(), err
	}
	return !resp.Close && !req.Close, body.n.Load(), down.n.Load(), nil
}

func writeInner(c net.Conn, status int, msg string) {
	resp := &http.Response{
		StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader(msg + "\n")),
		// Set so net/http writes a Content-Length rather than closing to signal
		// the end, which a client reads as a truncated answer.
		ContentLength: int64(len(msg) + 1),
	}
	if err := resp.Write(c); err != nil {
		slog.Debug("could not answer inside an intercepted connection", "err", err)
	}
}

// bufConn hands back the bytes the HTTP server already read.
//
// Hijack returns a buffered reader that may hold the first bytes of the TLS
// ClientHello — a client that pipelined its handshake behind the CONNECT. Wrap
// rather than discard: dropping them is a handshake that hangs until it times
// out, on some clients and not others, which is the worst way to find out.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }
