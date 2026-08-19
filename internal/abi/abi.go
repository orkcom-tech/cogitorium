// Package abi is the contract between this server and a plugin's backend.
//
// One contract, several transports. A plugin's code may run as a WebAssembly
// module inside this binary, as a supervised child process, or in a container
// on the sandbox — and none of that changes a line of what an author writes.
// That is the whole point of putting the vocabulary in one package: the tier a
// plugin lands on is the host's decision, so the tier must not be visible in
// the shape of the conversation.
//
// The shape is deliberately dull. Bytes in, bytes out, one JSON envelope each
// way. Nothing here is a Go type a plugin has to have a binding for, nothing
// depends on shared memory, and nothing needs a code generator — because the
// moment it does, "write a plugin in any language" becomes "write a plugin in
// the languages somebody generated bindings for".
package abi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Version is the contract integer. It moves only when this vocabulary breaks —
// never for an addition, because a plugin that would still work must not be
// refused. A manifest declares the version it speaks and the artifact exports
// it too, so a manifest that claims one thing while its code speaks another is
// caught by the code rather than believed.
const Version = 1

// Role is what an export is for.
//
// A closed set: an export whose role this build does not know is refused at
// load with the roles listed, rather than registered as something nobody calls.
type Role string

const (
	// RoleRoute answers an HTTP request under the plugin's own path space.
	RoleRoute Role = "route"
	// RoleProvider supplies a model for a template — the plugin's own page, or
	// a region it overrides. This is the export that makes a page dynamic
	// without its route, template name or URL changing.
	RoleProvider Role = "provider"
	// RoleFilter transforms a value the host is about to render or store. It
	// is called with what it would have used and returns what to use instead.
	RoleFilter Role = "filter"
	// RoleEvent observes something that happened. It cannot change the outcome;
	// an export that wants to needs to be a filter, and the distinction is
	// enforced by there being nowhere to put a decision in the reply.
	RoleEvent Role = "event"
	// RoleTool is a capability offered to an agent, with its own JSON Schema.
	RoleTool Role = "tool"
	// RoleSchedule runs on a timer the operator approved.
	RoleSchedule Role = "schedule"
	// RoleCommand is a human action, dispatched without a model turn.
	RoleCommand Role = "command"
)

var roles = map[Role]bool{
	RoleRoute: true, RoleProvider: true, RoleFilter: true, RoleEvent: true,
	RoleTool: true, RoleSchedule: true, RoleCommand: true,
}

// Roles lists the vocabulary, for a refusal that tells an author what they
// could have written.
func Roles() []string {
	return []string{
		string(RoleRoute), string(RoleProvider), string(RoleFilter),
		string(RoleEvent), string(RoleTool), string(RoleSchedule), string(RoleCommand),
	}
}

// ValidRole reports whether this build knows a role.
func ValidRole(r Role) bool { return roles[r] }

// ── host to plugin ────────────────────────────────────────────────────────

// Request is what a plugin's export receives. One shape for every role, so a
// runtime never has to know which role it is dispatching.
type Request struct {
	// Contract is the version the host is speaking. A plugin that reads a
	// number it does not know should refuse rather than guess.
	Contract int `json:"contract"`
	// Export is the name the author registered.
	Export string `json:"export"`
	Role   Role   `json:"role"`

	// Ctx is who and where, mirroring the template context so a backend and a
	// template describe the world with the same words.
	Ctx Ctx `json:"ctx"`

	// HTTP carries the request for a route export, absent otherwise.
	HTTP *HTTPRequest `json:"http,omitempty"`
	// Input is the role's own payload: a tool's arguments against its schema,
	// a filter's current value, an event's subject. Raw so a plugin decodes it
	// with its own types rather than through a shape this package invented.
	Input json.RawMessage `json:"input,omitempty"`
}

// Ctx is the same context a template gets. Deliberately the same fields: a
// plugin author who learned them in a template should not learn them twice.
type Ctx struct {
	Viewer      Viewer `json:"viewer"`
	Workspace   int64  `json:"workspace,omitempty"`
	InstallMode string `json:"install_mode,omitempty"`
	Path        string `json:"path,omitempty"`
	Locale      string `json:"locale,omitempty"`
}

// Viewer is who is asking, reduced to what a plugin legitimately needs. No
// token, no session, no email: a plugin that wants to act as somebody uses its
// own scoped credential, and one that wants to identify them has an id.
type Viewer struct {
	ID       int64  `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	IsAdmin  bool   `json:"is_admin,omitempty"`
	SignedIn bool   `json:"signed_in"`
}

// HTTPRequest is the part of a request a plugin may see.
type HTTPRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Params map[string]string `json:"params,omitempty"`
	Query  map[string]string `json:"query,omitempty"`
	// Header is an allowlist, not the request's headers. Cookies and the
	// Authorization header are never among them: a plugin that received the
	// operator's credential could act as them everywhere, which is the
	// opposite of it holding a scoped one.
	Header map[string]string `json:"header,omitempty"`
	Body   json.RawMessage   `json:"body,omitempty"`
}

// ── plugin to host ────────────────────────────────────────────────────────

// Response is what an export returns.
//
// Exactly one of Template, Content and Data carries the answer. Refusing to
// merge them is deliberate: a reply that set two would have a precedence rule,
// and a precedence rule is a thing every author has to learn and every runtime
// has to agree on.
type Response struct {
	// Template renders a named template through the host's layer stack, with
	// Model as its data. THIS is the branch that matters: a backend answering
	// this way participates in late binding, so its output can itself be
	// overridden by a plugin layered above it. A backend that emitted finished
	// HTML would be outside the mechanism the whole system is built on.
	Template string `json:"template,omitempty"`
	Model    any    `json:"model,omitempty"`

	// Content is a body the host passes through — a file, a redirect target,
	// anything that is not a rendered view.
	Content *Content `json:"content,omitempty"`

	// Data is a value for a role that returns one: a provider's model, a
	// filter's replacement, a tool's result.
	Data json.RawMessage `json:"data,omitempty"`

	// Status is an HTTP status for a route export. Zero means 200.
	Status int `json:"status,omitempty"`
	// Header is what to add to the reply, subject to the same allowlist as
	// what a plugin may read.
	Header map[string]string `json:"header,omitempty"`

	// Error is a refusal the plugin is making on purpose, distinct from a
	// crash. It reaches the operator as the plugin's own words.
	Error string `json:"error,omitempty"`
}

// Content is a raw body.
type Content struct {
	Type string `json:"type"`
	// Body is base64 in JSON, which is what encoding/json does with []byte.
	Body []byte `json:"body"`
}

// Validate reports whether a response is answerable.
func (r Response) Validate() error {
	set := 0
	if r.Template != "" {
		set++
	}
	if r.Content != nil {
		set++
	}
	if len(r.Data) > 0 {
		set++
	}
	if r.Error != "" {
		// A refusal is a complete answer on its own, and pairing it with a
		// body would leave the host deciding which one the author meant.
		if set > 0 {
			return fmt.Errorf("abi: a response carries an error and a body; a refusal is the whole answer")
		}
		return nil
	}
	if set > 1 {
		return fmt.Errorf("abi: a response sets more than one of template, content and data; " +
			"exactly one carries the answer")
	}
	if set == 0 && r.Status == 0 {
		return fmt.Errorf("abi: a response carries nothing — set template, content, data, a status, or an error")
	}
	return nil
}

// ── what a plugin may ask the host for ────────────────────────────────────

// Call is one name in the cog.* gateway. Every tier offers the identical set
// with identical semantics, so an author who outgrows one runtime and moves to
// another rewrites their build command and nothing else.
type Call string

const (
	// CallLog writes to the server's log, tagged with the plugin.
	CallLog Call = "log"
	// CallRender renders a named template through the layer stack, so a plugin
	// composing a fragment goes through the same machinery a page does.
	CallRender Call = "render"
	// CallHTTP makes an outbound request, checked against the hosts the
	// operator granted. There is no unchecked way out.
	CallHTTP Call = "http"
	// CallAPI calls this server's own API with the plugin's scoped token —
	// never the operator's.
	CallAPI Call = "api"
	// CallKV is the plugin's own durable storage.
	CallKV Call = "kv"
	// CallEnqueue schedules background work.
	CallEnqueue Call = "enqueue"
	// CallConfig reads the plugin's own settings.
	CallConfig Call = "config"
	// CallNow is the clock, and it is a host call rather than the guest's own
	// for one reason: `plugins invoke` pins it, so a plugin's output is
	// reproducible in a test. A guest reading its own clock cannot be.
	CallNow Call = "now"
	// CallRand is randomness, pinned the same way and for the same reason.
	CallRand Call = "rand"
)

var calls = map[Call]bool{
	CallLog: true, CallRender: true, CallHTTP: true, CallAPI: true, CallKV: true,
	CallEnqueue: true, CallConfig: true, CallNow: true, CallRand: true,
}

// Calls lists the gateway.
func Calls() []string {
	return []string{
		string(CallLog), string(CallRender), string(CallHTTP), string(CallAPI),
		string(CallKV), string(CallEnqueue), string(CallConfig), string(CallNow), string(CallRand),
	}
}

// ValidCall reports whether this build offers a call.
func ValidCall(c Call) bool { return calls[c] }

// HostRequest is a plugin asking the host for something.
type HostRequest struct {
	Call  Call            `json:"call"`
	Input json.RawMessage `json:"input,omitempty"`
}

// HostReply is the answer. Err is the host refusing — a denied host, a scope
// the plugin was not granted — and it is a value rather than a transport
// failure because a refusal is an ordinary thing a plugin has to handle.
type HostReply struct {
	Output json.RawMessage `json:"output,omitempty"`
	Err    string          `json:"error,omitempty"`
}

// Frame is what a guest writes on a tier that talks down a pipe.
//
// A guest may answer the request, or it may ask the host for something first
// and answer afterwards — as many times as it needs. Both travel as frames on
// the same channel, so exactly one of these fields is set and the reader
// switches on which.
//
// One channel rather than two, because a second pipe would need its own
// framing, its own deadline and its own failure mode, all to carry a
// conversation that is strictly sequential anyway: a guest waiting on a host
// reply is not doing anything else.
type Frame struct {
	// Host is the guest asking. The host answers with a HostReply frame and
	// the guest may then ask again.
	Host *HostRequest `json:"host,omitempty"`
	// Response ends the exchange.
	Response *Response `json:"response,omitempty"`
}

// Host is what a runtime implements. Every tier's implementation differs;
// what a plugin sees does not.
//
// One method rather than nine, because a runtime crossing a process or a
// WebAssembly boundary already has exactly one way to pass bytes, and nine
// methods would be nine places to keep that in step.
type Host interface {
	Call(plugin string, req HostRequest) HostReply
}

// KVOp names the storage operations. Compare-and-set and increment are here
// rather than left to read-then-write because two instances of a plugin WILL
// race, and an author should not have to discover that from a corrupted count.
type KVOp string

const (
	KVGet    KVOp = "get"
	KVSet    KVOp = "set"
	KVDelete KVOp = "delete"
	KVList   KVOp = "list"
	KVCAS    KVOp = "cas"
	KVIncr   KVOp = "incr"
)

// Export is one thing a plugin offers, as the host records it.
type Export struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
	// Schema is a JSON Schema, required for a tool and ignored elsewhere: an
	// agent needs to be told what arguments are legal, and nothing else here
	// is called by something that has to guess.
	Schema json.RawMessage `json:"schema,omitempty"`
	// Summary is what a person reads. For a tool it is what the model reads
	// too, which is why it is required there.
	Summary string `json:"summary,omitempty"`
}

// Validate checks one export declaration.
func (e Export) Validate() error {
	if e.Name == "" {
		return fmt.Errorf("abi: an export needs a name")
	}
	if !ValidRole(e.Role) {
		return fmt.Errorf("abi: export %q has unknown role %q — it must be one of: %s",
			e.Name, e.Role, strings.Join(Roles(), ", "))
	}
	if e.Role == RoleTool {
		if len(e.Schema) == 0 {
			return fmt.Errorf("abi: tool %q needs a JSON Schema: an agent has to be told "+
				"what arguments are legal", e.Name)
		}
		if e.Summary == "" {
			return fmt.Errorf("abi: tool %q needs a summary: it is what the model reads "+
				"to decide whether to call it", e.Name)
		}
	}
	return nil
}
