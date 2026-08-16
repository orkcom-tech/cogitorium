// Package mcpwire is the JSON-RPC 2.0 envelope both halves of MCP speak.
//
// It exists because there are two halves now. internal/mcp is the SERVER —
// this install exposed to somebody else's client over stdio. internal/mcpclient
// is the CLIENT — somebody else's server, spawned by this one, its tools
// offered to an agent. They send each other the same four shapes, and a second
// copy of those shapes is a second copy that drifts.
//
// The types moved here rather than being duplicated, and two things changed on
// the way, both of which were latent defects rather than preferences:
//
//   - the server's response type declared `Result any`, which encodes but
//     cannot decode. A client has to read a result, so it is json.RawMessage
//     here. A legitimate JSON null arrives as RawMessage("null") — four bytes,
//     so it survives omitempty — while an absent result stays absent;
//   - the error object dropped `data`, which JSON-RPC 2.0 defines and servers
//     use to say what actually went wrong.
package mcpwire

import "encoding/json"

// The JSON-RPC error codes, in the spec's own numbering.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// Message is one line on the wire, in either direction.
//
// One type rather than a request type and a response type, because a client
// reading a pipe does not know which is coming: an MCP server may send a
// request of its own (sampling/createMessage), a notification (tools/list_changed),
// or the answer to something we asked. Decoding into a shape that assumes one
// of those is how a client hangs waiting for a reply it already threw away.
type Message struct {
	JSONRPC string `json:"jsonrpc"`
	// ID is absent on a notification, which is the whole of how a notification
	// is recognised — and it must never be answered.
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// IsNotification reports a message that takes no answer.
//
// A literal `null` id counts as one. The spec says a notification has no id;
// it also says a response to an unparseable request carries a null id, so a
// message that arrives with `"id":null` is not something to reply to either.
// The server half treats null as a request, which is the asymmetry this
// function exists to stop spreading.
func (m Message) IsNotification() bool {
	return len(m.ID) == 0 || string(m.ID) == "null"
}

// IsRequest reports a message the other side expects an answer to: it names a
// method and carries an id.
func (m Message) IsRequest() bool { return m.Method != "" && !m.IsNotification() }

// IsResponse reports an answer to something we sent.
func (m Message) IsResponse() bool { return m.Method == "" && !m.IsNotification() }

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) > 0 {
		return e.Message + " (" + string(e.Data) + ")"
	}
	return e.Message
}

// Tool is one thing a server offers, in the shape MCP asks for. The same type
// describes a tool this install exposes and a tool it was offered.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}
