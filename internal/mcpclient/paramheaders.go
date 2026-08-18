package mcpclient

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Tool parameters a server asked to be mirrored into HTTP headers.
//
// # What this is for
//
// A server may annotate a parameter in its `inputSchema` with `x-mcp-header`,
// and a conforming client MUST copy that argument's value into a
// `Mcp-Param-{Name}` header on the call. The point is that a gateway in front
// of the server can route or rate-limit on it — `Mcp-Param-Region: us-west1` —
// without parsing the body.
//
// # Why the validation here is not optional politeness
//
// The header and the body are two sources of truth for the same value, and that
// is precisely the shape of bug this exists to prevent: a load balancer routing
// on the header while the server executes on the body. So a server MUST reject a
// mismatch, and a client MUST NOT create one.
//
// The constraints are therefore checked rather than trusted, and a tool that
// breaks them is EXCLUDED FROM THE LIST rather than called carefully — the spec
// says so, and the reason is that an annotation naming a header this client
// cannot construct is a tool whose every call would be refused. One malformed
// definition must not take the server's other tools with it.
type paramHeader struct {
	// header is the `Mcp-Param-{Name}` field name.
	header string
	// path is the chain of `properties` keys leading to the annotated value.
	path []string
}

// A header name is an HTTP token: no spaces, no controls, no separators.
var tokenRule = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

// headerParams reads a tool's schema and returns what it asked to have
// mirrored, or an error naming why the definition is unusable.
//
// A nil result with a nil error is the ordinary case: almost no tool annotates
// anything, and this must cost nothing when it does not.
func headerParams(schema json.RawMessage) ([]paramHeader, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		// Not this function's business to reject: a schema this cannot read is
		// a schema the model is offered as-is, exactly as before.
		return nil, nil
	}
	var found []paramHeader
	seen := map[string]bool{}
	if err := walkProperties(root, nil, &found, seen); err != nil {
		return nil, err
	}
	return found, nil
}

// walkProperties descends ONLY through `properties`.
//
// Not through `items`, not through `oneOf`/`anyOf`/`allOf`/`not`, not through
// `if`/`then`/`else`, and not through `$ref`. That is the spec's rule and it is
// not arbitrary: a value reachable only through an array index or a schema
// branch has no single path, so there is nothing for a header to mirror and
// nothing for a server to validate against.
func walkProperties(node map[string]any, path []string, out *[]paramHeader, seen map[string]bool) error {
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return nil
	}
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		here := append(append([]string{}, path...), name)
		if v, present := prop["x-mcp-header"]; present {
			header, ok := v.(string)
			if !ok || header == "" {
				return fmt.Errorf("property %q annotates x-mcp-header with something that is not a name",
					strings.Join(here, "."))
			}
			if !tokenRule.MatchString(header) {
				return fmt.Errorf("property %q annotates x-mcp-header %q, which is not a valid header name",
					strings.Join(here, "."), header)
			}
			// Case-insensitively unique, because HTTP field names are.
			if lower := strings.ToLower(header); seen[lower] {
				return fmt.Errorf("x-mcp-header %q is used twice in this tool's schema", header)
			} else {
				seen[lower] = true
			}
			switch prop["type"] {
			case "string", "integer", "boolean":
			case "number":
				// Excluded by the spec, and for a good reason: a server
				// comparing 42.0 against "42.0" has to decide what equality
				// means for a float written as text.
				return fmt.Errorf("property %q annotates x-mcp-header and is a number, which may not be mirrored",
					strings.Join(here, "."))
			default:
				return fmt.Errorf("property %q annotates x-mcp-header and is %v, which is not a primitive",
					strings.Join(here, "."), prop["type"])
			}
			*out = append(*out, paramHeader{header: header, path: here})
		}
		if err := walkProperties(prop, here, out, seen); err != nil {
			return err
		}
	}
	return nil
}

// paramHeaderValues extracts what the call actually carries at each annotated
// path, encoded the way it will travel.
//
// A path with no value present is OMITTED, and that is the contract rather than
// a convenience: the server must not expect a header for an argument that was
// not supplied, and sending an empty one would be a mismatch.
func paramHeaderValues(params []paramHeader, args json.RawMessage) map[string]string {
	if len(params) == 0 || len(args) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(args, &root); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range params {
		v, ok := at(root, p.path)
		if !ok || v == nil {
			continue
		}
		s, ok := scalarText(v)
		if !ok {
			continue
		}
		out["Mcp-Param-"+p.header] = headerSafe(s)
	}
	return out
}

func at(root map[string]any, path []string) (any, bool) {
	var cur any = root
	for _, step := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[step]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// scalarText is the spec's type conversion: a string as-is, an integer in
// decimal, a boolean lower-case.
//
// JSON has one number type, so an integer arrives as a float64 and has to be
// checked for being whole — a value that is not is one this client must not
// mirror, because `number` may not be annotated in the first place.
func scalarText(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		if t != float64(int64(t)) {
			return "", false
		}
		return strconv.FormatInt(int64(t), 10), true
	case json.Number:
		return t.String(), true
	}
	return "", false
}
