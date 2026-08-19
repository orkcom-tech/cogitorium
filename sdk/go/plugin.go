// Package cogitorium is what you write a Cogitorium plugin against.
//
// One API, two tiers. The same file compiles to a native binary the server
// supervises as a child process, and to a WebAssembly module the server runs
// inside itself:
//
//	go build -o bin/myplugin .                        # native
//	GOOS=wasip1 GOARCH=wasm go build -o plugin.wasm . # wasm
//	tinygo build -target=wasip1 -o plugin.wasm .      # wasm, smaller
//
// Nothing in a plugin changes between those lines, which is the promise the
// whole plugin system is built on: the tier is the operator's decision, made
// when they approve an install, and a decision the operator makes must not be
// a decision the author had to write code for.
//
// A plugin looks like this:
//
//	var plugin = cogitorium.New("myplugin").
//		Provider("home", func(r *cogitorium.Request, h *cogitorium.Host) (any, error) {
//			return map[string]any{"greeting": "Hello, " + r.Ctx.Viewer.Name}, nil
//		})
//
//	func main() { plugin.Run() }
//
// Exports are registered at PACKAGE level, not inside main, and that is not a
// style preference. On the WebAssembly tier the host loads a module and calls
// into it — it never runs main, because a main that ran would also end, and a
// module whose main has ended cannot be called. Package-level initialisation
// is the one thing that happens on both tiers, so it is where registration
// goes.
//
// Run serves until the host stops asking. On wasm there is nothing to serve
// and it returns immediately, so the same main is correct on both.
package cogitorium

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Contract is the ABI generation this SDK speaks. The host checks it at the
// handshake and refuses a mismatch there, rather than discovering it halfway
// through a call when half an answer has already been written.
const Contract = 1

// Request is one call from the host. One shape for every role, so a plugin
// that grows a second kind of export does not grow a second kind of function.
type Request struct {
	Export string          `json:"export"`
	Role   string          `json:"role"`
	Ctx    Ctx             `json:"ctx"`
	HTTP   *HTTPRequest    `json:"http,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
}

// Ctx is who is asking and where. The same field names a template sees, on
// purpose: an author who learned them in a template should not learn them
// twice.
type Ctx struct {
	Viewer      Viewer `json:"viewer"`
	Workspace   int64  `json:"workspace,omitempty"`
	InstallMode string `json:"install_mode,omitempty"`
	Path        string `json:"path,omitempty"`
	Locale      string `json:"locale,omitempty"`
}

// Viewer is who is asking, reduced to what a plugin legitimately needs. No
// token and no session: a plugin that wants to act as somebody uses its own
// scoped credential.
type Viewer struct {
	ID       int64  `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	IsAdmin  bool   `json:"is_admin,omitempty"`
	SignedIn bool   `json:"signed_in"`
}

// HTTPRequest is the part of a request a route export may see. Header is an
// allowlist rather than the request's headers — never a cookie, never the
// Authorization header.
type HTTPRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Params map[string]string `json:"params,omitempty"`
	Query  map[string]string `json:"query,omitempty"`
	Header map[string]string `json:"header,omitempty"`
	Body   json.RawMessage   `json:"body,omitempty"`
}

// Viewer shortcuts, because r.Ctx.Viewer.Name reads like plumbing at the point
// where a plugin is deciding what to show somebody.

// Args decodes a task's or a tool's arguments into v.
func (r *Request) Args(v any) error {
	if len(r.Input) == 0 {
		return nil
	}
	return json.Unmarshal(r.Input, v)
}

// Response is what an export returns when it wants more than a model.
//
// Most exports return a plain value and never touch this. Reach for it to
// render a named template through the layer stack, to answer with a file, or
// to set a status.
type Response struct {
	// Template renders through the host's layer stack with Model as its data,
	// so what this plugin produces can itself be overridden by a plugin
	// layered above it. Emitting finished HTML instead would put the answer
	// outside the mechanism the whole system runs on.
	Template string `json:"template,omitempty"`
	Model    any    `json:"model,omitempty"`

	Content *Content `json:"content,omitempty"`

	Data json.RawMessage `json:"data,omitempty"`

	Status int               `json:"status,omitempty"`
	Header map[string]string `json:"header,omitempty"`

	Error string `json:"error,omitempty"`
}

// Content is a raw body — a file, a redirect target, anything that is not a
// rendered view.
type Content struct {
	Type string `json:"type"`
	Body []byte `json:"body"`
}

// Export is a plugin's function. Return any JSON-marshalable value and it
// becomes the model; return a *Response to say more than that.
//
// An error is a refusal in the plugin's own words, and it reaches the operator
// as those words rather than as "plugin failed".
type Export func(r *Request, h *Host) (any, error)

// Plugin is the set of exports and the loop, if the tier has one.
type Plugin struct {
	ID      string
	exports map[string]Export
}

// New starts a plugin. The id must be the one in plugin.yaml — the host tags
// this plugin's log lines, storage and scoped credential with it.
func New(id string) *Plugin {
	p := &Plugin{ID: id, exports: map[string]Export{}}
	adopt(p)
	return p
}

// Provider registers an export that supplies a page's model.
func (p *Plugin) Provider(name string, fn Export) *Plugin { return p.add(name, fn) }

// Route registers an export that answers an HTTP request under the plugin's
// own path space.
func (p *Plugin) Route(name string, fn Export) *Plugin { return p.add(name, fn) }

// Tool registers a capability offered to an agent.
func (p *Plugin) Tool(name string, fn Export) *Plugin { return p.add(name, fn) }

// Task registers an export run in the background — by the queue, or on a
// schedule the operator approved.
//
// Four words for one map because an author reading their own file should be
// able to tell which of their functions answer a person and which do not. The
// role is declared in plugin.yaml either way; these do not set it.
func (p *Plugin) Task(name string, fn Export) *Plugin { return p.add(name, fn) }

// Each returns the plugin so a whole plugin can be one package-level
// declaration — which on the WebAssembly tier is not a convenience but the
// only place registration can happen at all.
func (p *Plugin) add(name string, fn Export) *Plugin {
	p.exports[name] = fn
	return p
}

// dispatch runs one request and produces the response, whatever the transport
// underneath. Every branch here is shared by both tiers on purpose: a bug
// fixed in one is not a bug still present in the other.
func (p *Plugin) dispatch(raw []byte, h *Host) Response {
	var r Request
	if err := json.Unmarshal(raw, &r); err != nil {
		return Response{Error: "the request envelope is not readable: " + err.Error()}
	}

	fn, ok := p.exports[r.Export]
	if !ok {
		if len(p.exports) == 0 {
			// The one mistake this shape invites, answered with the fix.
			// Registering inside main works when you test the binary and
			// silently registers nothing once the same code is a module.
			return Response{Error: p.ID + " registered no exports. On the WebAssembly tier the " +
				"host never runs main, so register at package level: " +
				`var plugin = cogitorium.New("` + p.ID + `").Provider("home", home)`}
		}
		// Named, with what does exist. An author whose export is never called
		// has otherwise no way to tell a typo from a host that never asked.
		return Response{Error: fmt.Sprintf("%s has no export %q; it has: %s",
			p.ID, r.Export, p.names())}
	}

	out, err := fn(&r, h)
	if err != nil {
		return Response{Error: err.Error()}
	}
	switch v := out.(type) {
	case nil:
		return Response{Data: json.RawMessage("{}")}
	case *Response:
		if v == nil {
			return Response{Data: json.RawMessage("{}")}
		}
		return *v
	case Response:
		return v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return Response{Error: "what it returned is not JSON: " + err.Error()}
		}
		return Response{Data: data}
	}
}

func (p *Plugin) names() string {
	if len(p.exports) == 0 {
		return "none"
	}
	names := make([]string, 0, len(p.exports))
	for name := range p.exports {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ── the host ──────────────────────────────────────────────────────────────

// Host is what a plugin may ask the server for: nine calls, identical on every
// tier, so a plugin that outgrows Go and is rewritten in Rust calls the same
// nine things.
type Host struct {
	// ask carries one host call over whatever this tier uses for bytes. It is
	// the ONLY thing that differs between native and wasm.
	ask func(body []byte) ([]byte, error)
}

// Error is the host refusing, in its own words — a host that was not granted,
// a scope the operator did not approve. It is a value a plugin handles, not a
// crash: being told no is an ordinary thing.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func (h *Host) call(name string, input any, out any) error {
	if input == nil {
		input = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{"call": name, "input": input})
	if err != nil {
		return err
	}
	raw, err := h.ask(body)
	if err != nil {
		return err
	}
	var reply struct {
		Output json.RawMessage `json:"output"`
		Err    string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return err
	}
	if reply.Err != "" {
		return &Error{Message: reply.Err}
	}
	if out == nil || len(reply.Output) == 0 {
		return nil
	}
	return json.Unmarshal(reply.Output, out)
}

// Log writes to the server's log, tagged with this plugin.
func (h *Host) Log(message string) error { return h.call("log", message, nil) }

// Now is the host's clock, as an RFC3339 string. The host's rather than this
// process's so that `cogitorium plugins invoke` can pin it: a plugin reading
// its own clock cannot be reproduced in a test.
func (h *Host) Now() (string, error) {
	var out struct {
		RFC3339 string `json:"rfc3339"`
	}
	err := h.call("now", nil, &out)
	return out.RFC3339, err
}

// Rand returns an integer in [0, max). Pinnable, like Now, and for the same
// reason.
func (h *Host) Rand(max int64) (int64, error) {
	var out struct {
		N int64 `json:"n"`
	}
	err := h.call("rand", map[string]any{"max": max}, &out)
	return out.N, err
}

// Config is what the operator set for this plugin. Read-only, and often empty.
func (h *Host) Config(into any) error { return h.call("config", nil, into) }

// Render renders one of this plugin's templates through the layer stack.
//
// Through the stack, so another plugin's override of the same name is what
// comes back — which is the point. Rendering your own file in isolation would
// be quietly wrong in exactly the case the system exists for.
func (h *Host) Render(template string, data any) (string, error) {
	var out struct {
		HTML string `json:"html"`
	}
	err := h.call("render", map[string]any{"template": template, "data": data}, &out)
	return out.HTML, err
}

// HTTPResponse is what an outbound request came back with.
type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// HTTP makes one outbound request through the host's gate. Only hosts listed
// under `hosts:` in plugin.yaml; the refusal names both what you asked for and
// what you were granted.
func (h *Host) HTTP(method, url string, headers map[string]string, body []byte) (*HTTPResponse, error) {
	if method == "" {
		method = "GET"
	}
	var out struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	err := h.call("http", map[string]any{
		"url": url, "method": method, "headers": headers,
		"body": base64.StdEncoding.EncodeToString(body),
	}, &out)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Body)
	if err != nil {
		return nil, err
	}
	return &HTTPResponse{Status: out.Status, Headers: out.Headers, Body: decoded}, nil
}

// APIResponse is what this server's own API answered.
type APIResponse struct {
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"`
}

// API calls this server's own API as this plugin, never as the operator. Only
// subjects listed under `api:` in plugin.yaml; a write grant implies the
// matching read.
func (h *Host) API(method, path string, body any) (*APIResponse, error) {
	if method == "" {
		method = "GET"
	}
	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	err := h.call("api", map[string]any{"method": method, "path": path, "body": body}, &out)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Body)
	if err != nil {
		return nil, err
	}
	return &APIResponse{Status: out.Status, Body: decoded}, nil
}

// Enqueue runs one of your own exports later, on the host's durable queue.
//
// key makes it idempotent, so enqueuing on every start does not accumulate one
// task per restart. after is a delay in seconds.
func (h *Host) Enqueue(export string, args any, after int, key string) error {
	return h.call("enqueue", map[string]any{
		"export": export, "args": args, "after": after, "key": key,
	}, nil)
}

// ── storage ───────────────────────────────────────────────────────────────

// Get returns the stored bytes. found is false when there is nothing there:
// absent is a value, not an error.
func (h *Host) Get(key string) (value []byte, found bool, err error) {
	var out struct {
		Found bool   `json:"found"`
		Value string `json:"value"`
	}
	if err := h.call("kv", map[string]any{"op": "get", "key": key}, &out); err != nil {
		return nil, false, err
	}
	if !out.Found {
		return nil, false, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Value)
	return decoded, err == nil, err
}

// Set stores bytes under a key.
func (h *Host) Set(key string, value []byte) error {
	return h.call("kv", map[string]any{
		"op": "set", "key": key, "value": base64.StdEncoding.EncodeToString(value),
	}, nil)
}

// Delete removes a key. Removing one that was never there is not an error.
func (h *Host) Delete(key string) error {
	return h.call("kv", map[string]any{"op": "delete", "key": key}, nil)
}

// Incr adds to a counter in one statement, so two instances of a plugin cannot
// lose one of their increments to a read-modify-write race. They WILL race,
// and an author should not have to learn that from a wrong number.
func (h *Host) Incr(key string, by int64) (int64, error) {
	// The count comes back as a string. A JSON number would be a float on the
	// way through every language that has only one number type, and a counter
	// that silently stops being exact past 2^53 is worse than a string.
	var out struct {
		Value string `json:"value"`
	}
	if err := h.call("kv", map[string]any{"op": "incr", "key": key, "by": by}, &out); err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out.Value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("the counter at %q does not hold a number: %q", key, out.Value)
	}
	return n, nil
}

// Keys lists the keys under a prefix.
func (h *Host) Keys(prefix string) ([]string, error) {
	var out struct {
		Keys []struct {
			Key string `json:"key"`
		} `json:"keys"`
	}
	if err := h.call("kv", map[string]any{"op": "list", "prefix": prefix}, &out); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(out.Keys))
	for _, row := range out.Keys {
		keys = append(keys, row.Key)
	}
	return keys, nil
}

// CompareAndSet writes only if the stored version is still the one you read.
//
// Version rather than value: two writers who happen to write identical bytes
// are still two writers, and comparing values cannot tell them apart. Pass
// version 0 to mean "only if it does not exist yet".
func (h *Host) CompareAndSet(key string, value []byte, version int64) (bool, error) {
	var out struct {
		Swapped bool `json:"swapped"`
	}
	err := h.call("kv", map[string]any{
		"op": "cas", "key": key, "version": version,
		"value": base64.StdEncoding.EncodeToString(value),
	}, &out)
	return out.Swapped, err
}
