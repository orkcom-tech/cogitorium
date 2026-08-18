package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
	"sync"
	"time"
)

// stdioBackend is a child process on this host, spoken to over its standard
// streams — the transport 0027 built and the only one that puts somebody else's
// code on this machine.
//
// A pipe is full duplex, so one reader goroutine owns stdout for the life of
// the connection and hands every line to Conn.handle. That is the difference
// from the remote transports, where a reply belongs to the request that asked
// for it and there is no standing stream at all.
type stdioBackend struct {
	name string
	conn *Conn

	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader

	writeMu sync.Mutex

	stderr *tailBuffer
	// stderrDone closes when the child's stderr has been drained to EOF. The
	// death path waits on it, because the reason a child died arrives on a
	// DIFFERENT pipe from the EOF that reveals it died.
	stderrDone chan struct{}

	release func()
	end     context.CancelFunc

	closeOnce sync.Once
}

func (b *stdioBackend) send(m mcpwire.Message) error {
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if _, err := b.in.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write to the MCP server %q: %w", b.name, err)
	}
	return nil
}

// explain quotes the last thing the child said, because a server that dies on
// startup says why on stderr and nowhere else. Without it the operator gets
// "exit status 1", which is true and useless.
func (b *stdioBackend) explain(err error) string {
	last := b.stderr.lastLine()
	switch {
	case last != "" && err != nil:
		return fmt.Sprintf("%v; it last said: %s", err, last)
	case last != "":
		return "it last said: " + last
	default:
		return fmt.Sprintf("%v", err)
	}
}

// close asks the child to stop, then makes sure it did.
func (b *stdioBackend) close() {
	b.closeOnce.Do(func() {
		// Closing stdin is how an MCP server is asked to stop. Cancelling the
		// connection's context is what happens to one that does not: procgroup
		// turned that into a kill of the whole group, so a wrapper that exec'd
		// the real server does not leave it behind.
		_ = b.in.Close()
		select {
		case <-b.conn.dead:
		case <-time.After(3 * time.Second):
		}
		b.end()
		_ = b.cmd.Wait()
		b.release()
	})
}

// read is the only reader of the pipe, and it classifies every line before
// doing anything with it.
func (b *stdioBackend) read() {
	for {
		line, err := b.out.ReadBytes('\n')
		if len(line) > 0 {
			b.conn.handle(line)
		}
		if err != nil {
			// Drain stderr BEFORE releasing anybody. A child that gives up
			// writes the reason on stderr and then exits; the exit is what
			// closes stdout and lands us here. Those are two independent pipes
			// with two independent readers, so without this wait the waiters
			// are woken and build their error from a tail buffer the copier has
			// not filled in yet — the caller is told "stopped: EOF" and the
			// sentence explaining why arrives microseconds later, into nothing.
			select {
			case <-b.stderrDone:
			case <-time.After(stderrGrace):
				// A grandchild holding the pipe open would otherwise wedge
				// every waiter here. Losing the quote beats not answering.
			}
			b.conn.die(err)
			return
		}
	}
}
