package gearnet

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
)

// References: a gear is given a stand-in, and the real value is put back at the
// edge.
//
// Before this, a gear's secrets were decrypted and put in its environment. The
// process this whole package treats as untrusted — agent-authored code, running
// because an operator approved a source listing — held the real credential in
// memory, and everything after that was a matter of it behaving. The redactor
// covered what the software printed; it could not cover what the code chose to
// do with a string it had.
//
// Now the environment carries a per-run stand-in, and the gate swaps it for the
// real value on the way out. The cryptography is unchanged — the same
// AES-256-GCM and the same key derivation, in internal/secrets — because what
// moves here is the injection point, not the storage.
//
// The stand-in is worth nothing on its own: it is random per run, it is only
// known to this gate, and it stops meaning anything the moment the ticket
// closes. A gear that exfiltrates it has exfiltrated a string that will not
// open the door it names, from anywhere, ever again.
const refPrefix = "cogitorium-secret-"

// maxSubstitutedBody bounds what is buffered to rewrite a request body.
//
// A credential travels in a header, a query string or a small JSON body. A
// hundred-megabyte upload is not carrying one, and buffering it to look would
// turn the gate into the thing that runs the machine out of memory. Past this
// point the body is still rewritten — see substituteReader — it is simply
// streamed rather than measured, so the request goes out chunked.
const maxSubstitutedBody = 1 << 20

// Reference mints a stand-in for one value, for the life of this ticket.
//
// The same value asked for twice gets the same stand-in, so a gear declaring
// two names that happen to hold one credential does not see two different
// strings and conclude they are different credentials.
func (t *Ticket) Reference(value string) string {
	if t == nil || value == "" {
		return value
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for ref, v := range t.refs {
		if v == value {
			return ref
		}
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		// Without randomness there is no stand-in worth having, so the caller
		// gets the real value rather than a guessable placeholder. This branch
		// does not occur on any platform this runs on.
		return value
	}
	ref := refPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if t.refs == nil {
		t.refs = map[string]string{}
	}
	t.refs[ref] = value
	return ref
}

// Intercepts reports whether this run's TLS has to be read inside for its
// stand-ins to become real values. It is what tells the executor to hand the
// run the gate's certificate: without that, the gear's own client refuses the
// connection, correctly, and the run fails on a certificate error.
func (t *Ticket) Intercepts() bool { return t.holdsReferences() }

// holdsReferences reports whether anything about this run needs the gate to
// read inside its requests. It is the switch for TLS interception: a run with
// nothing to substitute is tunnelled exactly as before, opaque to the gate.
func (t *Ticket) holdsReferences() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.refs) > 0
}

func (t *Ticket) replacer() *strings.Replacer {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.refs) == 0 {
		return nil
	}
	pairs := make([]string, 0, len(t.refs)*2)
	for ref, value := range t.refs {
		pairs = append(pairs, ref, value)
	}
	return strings.NewReplacer(pairs...)
}

// substitute rewrites one outbound request in place.
//
// Headers, the URL and the body, because those are the three places an API key
// is put. Header names are left alone — a stand-in is a value, never a name.
func (t *Ticket) substitute(r *http.Request) {
	rep := t.replacer()
	if rep == nil {
		return
	}
	for _, vs := range r.Header {
		for i, v := range vs {
			vs[i] = rep.Replace(v)
		}
	}
	if r.URL != nil {
		r.URL.RawQuery = rep.Replace(r.URL.RawQuery)
		r.URL.Path = rep.Replace(r.URL.Path)
		r.URL.RawPath = ""
	}
	if r.Body == nil || r.ContentLength == 0 {
		return
	}

	// Read a bounded prefix. If the whole body fits, the rewritten length is
	// known exactly and the request keeps its Content-Length — which matters,
	// because some services refuse a chunked body outright.
	head := make([]byte, maxSubstitutedBody+1)
	n, _ := io.ReadFull(r.Body, head)
	head = head[:n]
	if n <= maxSubstitutedBody {
		body := []byte(rep.Replace(string(head)))
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Del("Content-Length")
		return
	}
	// Longer than the bound: rewritten as it streams, and the length is no
	// longer knowable in advance.
	rest := r.Body
	r.Body = io.NopCloser(substituteReader(io.MultiReader(bytes.NewReader(head), rest), t))
	r.ContentLength = -1
	r.Header.Del("Content-Length")
}

// substituteReader rewrites a stream it cannot hold.
//
// The only hard part is a stand-in split across two reads. Holding back a fixed
// number of trailing bytes is the obvious answer and it is wrong: a stand-in
// starting BEFORE the cut and ending after it is emitted in halves, neither of
// which matches. So each chunk is emitted only up to the last position where
// its remainder could still become one, and that remainder is carried forward.
//
// Found by a test that put a stand-in one byte over a 32 KiB boundary. The
// fixed-offset version substituted correctly on every payload except the ones
// cut at exactly the wrong place, which is the worst kind of correct.
func substituteReader(r io.Reader, t *Ticket) io.Reader {
	rep := t.replacer()
	if rep == nil {
		return r
	}
	t.mu.Lock()
	refs := make([]string, 0, len(t.refs))
	longest := 0
	for ref := range t.refs {
		refs = append(refs, ref)
		if len(ref) > longest {
			longest = len(ref)
		}
	}
	t.mu.Unlock()

	pr, pw := io.Pipe()
	go func() {
		var tail []byte
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := make([]byte, 0, len(tail)+n)
				chunk = append(append(chunk, tail...), buf[:n]...)
				cut := heldBack(chunk, refs, longest)
				if cut > 0 {
					if _, werr := io.WriteString(pw, rep.Replace(string(chunk[:cut]))); werr != nil {
						pw.CloseWithError(werr)
						return
					}
				}
				tail = chunk[cut:]
			}
			if err != nil {
				if len(tail) > 0 {
					_, _ = io.WriteString(pw, rep.Replace(string(tail)))
				}
				if err == io.EOF {
					pw.Close()
				} else {
					pw.CloseWithError(err)
				}
				return
			}
		}
	}()
	return pr
}

// heldBack returns where to stop emitting: the earliest position in the last
// few bytes from which the remainder is still the beginning of some stand-in.
// Everything from there is carried into the next chunk.
func heldBack(chunk []byte, refs []string, longest int) int {
	from := len(chunk) - longest + 1
	if from < 0 {
		from = 0
	}
	for i := from; i < len(chunk); i++ {
		rest := chunk[i:]
		for _, ref := range refs {
			if len(rest) < len(ref) && strings.HasPrefix(ref, string(rest)) {
				return i
			}
		}
	}
	return len(chunk)
}

// parseIP is net.ParseIP, named here so ca.go does not import net for one call.
func parseIP(host string) net.IP { return net.ParseIP(host) }
