//go:build !wasm

package cogitorium

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// The native tier: a child process the server supervises, talking down its own
// stdin and stdout.
//
// Four bytes of length then JSON, which is small enough that an author could
// implement it by hand — and every author who does gets the same three things
// wrong: forgetting to flush, reading a body in one call that can return
// short, and answering without the frame wrapper. That is what this file is
// for.

const maxFrame = 8 << 20

// Run serves until the host closes the pipe.
//
// Stdout belongs to the protocol from here on. A plugin that prints to it is a
// plugin writing garbage into the middle of a frame, so print to stderr — or
// better, use Host.Log, which lands in the server's log tagged with this
// plugin and is therefore findable.
func (p *Plugin) Run() {
	if err := p.serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (p *Plugin) serve(in io.Reader, out io.Writer) error {
	// Hello first, stating the contract this code speaks. Stated by the code
	// rather than believed from the manifest: a manifest can be wrong about
	// its own binary, and the host would rather refuse at startup than
	// discover it mid-call.
	if err := writeFrame(out, map[string]any{"contract": Contract, "plugin": p.ID}); err != nil {
		return err
	}

	host := &Host{ask: func(body []byte) ([]byte, error) {
		if err := writeFrame(out, json.RawMessage(`{"host":`+string(body)+`}`)); err != nil {
			return nil, err
		}
		reply, err := readFrame(in)
		if err != nil {
			return nil, err
		}
		if reply == nil {
			// The host went away mid-call. There is nobody left to answer, so
			// there is nothing to report to.
			os.Exit(0)
		}
		return reply, nil
	}}

	for {
		raw, err := readFrame(in)
		if err != nil {
			return err
		}
		if raw == nil {
			return nil
		}
		resp := p.dispatch(raw, host)
		if err := writeFrame(out, map[string]any{"response": resp}); err != nil {
			return err
		}
	}
}

func writeFrame(out io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > maxFrame {
		return fmt.Errorf("a %d byte frame is past the %d byte limit", len(body), maxFrame)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := out.Write(header[:]); err != nil {
		return err
	}
	if _, err := out.Write(body); err != nil {
		return err
	}
	// os.Stdout is unbuffered, so there is nothing to flush here — but a
	// Flusher shows up the moment somebody wraps it, in a test or in a plugin
	// that buffers on purpose, and the symptom of missing this is a plugin
	// that "hangs" with nothing wrong in it.
	if f, ok := out.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// readFrame returns nil, nil at end of pipe: the host closing is how a plugin
// is told to stop, not a failure.
func readFrame(in io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(in, header[:]); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > maxFrame {
		return nil, fmt.Errorf("the host announced a %d byte frame", size)
	}
	body := make([]byte, size)
	// ReadFull rather than Read: a pipe read returns what is available, not
	// what was asked for. Assuming otherwise works on every small message and
	// fails on the first large one, which is the worst possible distribution
	// of failures.
	if _, err := io.ReadFull(in, body); err != nil {
		return nil, err
	}
	return body, nil
}

// adopt is nothing here. On the native tier the plugin reaches its loop
// through main, so there is no need for the package to hold on to it.
func adopt(*Plugin) {}
