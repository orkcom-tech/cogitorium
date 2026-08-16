// Package mcp serves this install's capabilities over the Model Context
// Protocol, so that Claude Desktop, Cursor or anything else speaking MCP can
// use them as tools.
//
// What is exposed, and why those two things:
//
//   - **Receiver tasks.** A receiver is already the designed door for somebody
//     else's system: it has an address, a key, a JSON Schema checked before any
//     model is called, a queue, and a row in the ledger for every delivery. An
//     MCP client is exactly the kind of caller it was built for, so this adds a
//     transport rather than a new way in.
//   - **Approved gears.** A gear is literally a tool — a name, a description
//     and an argument schema — which is the whole of an MCP tool definition.
//     Only approved ones appear, and calling one goes through the route that
//     enforces approval rather than the dry run that deliberately does not.
//
// What is NOT exposed, on purpose: the management API. Creating agents, drawing
// wires, editing prohibitions and approving gears are the operator's acts. An
// MCP client is a guest with a tool list, and a guest that can approve its own
// tools is not one.
//
// The transport is stdio, which is what MCP clients spawn. This process holds
// no database: it talks to a running Cogitorium over HTTP, so two processes
// never share one SQLite file — a constraint this product takes seriously
// enough to pin its Helm chart to a single replica over it.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
)

// The protocol version this speaks. MCP negotiates: the client states one, the
// server answers with what it will actually use.
const protocolVersion = "2024-11-05"

// JSON-RPC 2.0 error codes. The first four are the specification's; -32000 is
// the implementation-defined range, used for a tool that failed on its own
// terms rather than a malformed request.
const (
	codeParse          = mcpwire.CodeParse
	codeInvalidRequest = mcpwire.CodeInvalidRequest
	codeMethodNotFound = mcpwire.CodeMethodNotFound
	codeInvalidParams  = mcpwire.CodeInvalidParams
	codeInternal       = mcpwire.CodeInternal
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError = mcpwire.RPCError

// Tool is one thing this server offers, in the shape MCP asks for. The type
// lives in mcpwire because the client half reads the same shape from somebody
// else's server, and one definition is the point of putting it there.
type Tool = mcpwire.Tool

// Backend is what the protocol needs from the rest of the product. An interface
// rather than the client itself so the protocol can be tested by driving it,
// without a server or a network.
type Backend interface {
	// Tools is the current list. Called on every tools/list rather than cached:
	// a gear approved while the MCP client is connected should appear on the
	// next look, not on the next restart.
	Tools(ctx context.Context) ([]Tool, error)
	// Call runs one and returns what it produced as text. The error is for a
	// call that could not be made at all; a tool that ran and failed reports
	// that through isError.
	Call(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error)
}

// Server speaks MCP over a pair of streams.
type Server struct {
	backend Backend
	// out is serialised: MCP allows a client to have several calls in flight,
	// and two goroutines writing JSON to one pipe produce neither message.
	mu  sync.Mutex
	out *json.Encoder
}

func NewServer(backend Backend, w io.Writer) *Server {
	return &Server{backend: backend, out: json.NewEncoder(w)}
}

// Serve reads requests until the stream ends.
//
// One line per message, which is the framing MCP's stdio transport uses. A
// client that goes away closes the pipe and this returns — there is nothing to
// clean up, because this process owns no state of its own.
func (s *Server) Serve(ctx context.Context, r io.Reader) error {
	sc := bufio.NewScanner(r)
	// A tool's arguments can be large — a document to summarise, a file to
	// classify — and the default 64 KiB would truncate the request into a parse
	// error that reads as malformed JSON rather than as "too big".
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.fail(nil, codeParse, "that is not JSON: "+err.Error())
			continue
		}
		s.dispatch(ctx, req)
	}
	return sc.Err()
}

func (s *Server) dispatch(ctx context.Context, req request) {
	// A notification has no id and takes no answer — "notifications/initialized"
	// is the one every client sends. Replying to it is a protocol error, not a
	// harmless extra.
	notification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.reply(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "cogitorium", "version": Version},
		})
	case "tools/list":
		tools, err := s.backend.Tools(ctx)
		if err != nil {
			s.fail(req.ID, codeInternal, err.Error())
			return
		}
		if tools == nil {
			// An empty list must marshal as [] rather than null: a client that
			// reads null as "no tools object" reports the server as broken
			// instead of as empty.
			tools = []Tool{}
		}
		s.reply(req.ID, map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req.ID, codeInvalidParams, "params must be an object with name and arguments: "+err.Error())
			return
		}
		if p.Name == "" {
			s.fail(req.ID, codeInvalidParams, "a tool call needs a name")
			return
		}
		text, isErr, err := s.backend.Call(ctx, p.Name, p.Arguments)
		if err != nil {
			// The call could not be made — an unknown tool, an unreachable
			// server. That is a protocol-level failure.
			s.fail(req.ID, codeInvalidParams, err.Error())
			return
		}
		// The tool ran. Whether it succeeded is reported inside the result, so
		// the model sees the failure and can react to it — which is the whole
		// difference between a tool that failed and a call that never happened.
		s.reply(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		})
	default:
		if notification {
			return // nothing to answer, and nothing to complain about
		}
		s.fail(req.ID, codeMethodNotFound, fmt.Sprintf("this server does not implement %q", req.Method))
	}
}

// reply encodes the result before it goes into the envelope.
//
// The envelope now carries json.RawMessage so the client half can DECODE one,
// and encoding here rather than there keeps every caller writing ordinary Go
// values. A result that cannot be encoded is this server's own fault, so it
// becomes an internal error rather than a line that is not JSON.
func (s *Server) reply(id json.RawMessage, result any) {
	if len(id) == 0 {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		s.fail(id, codeInternal, "this server could not encode its own answer: "+err.Error())
		return
	}
	s.write(response{JSONRPC: "2.0", ID: id, Result: raw})
}

func (s *Server) fail(id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		// A malformed notification has nobody to answer. The specification says
		// so, and answering anyway is how a client ends up with a reply it
		// cannot match to anything.
		return
	}
	s.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *Server) write(r response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.out.Encode(r)
}

// Version is stamped by the command so the server can name itself honestly in
// initialize without this package importing the version package.
var Version = "dev"
