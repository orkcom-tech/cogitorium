package inlet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"sort"
	"strings"
	"unicode/utf8"
)

// A task's payload is checked here, before any model is called. That ordering
// is the promise the door makes: a body that does not match is refused for
// nothing, not after a paid provider call and a half-done job.
//
// This validates a SUBSET of JSON Schema, so the subset is enforced at BOTH
// ends: a schema using a keyword this file does not implement is refused when
// the task is created, not ignored when a payload arrives. That refusal is the
// whole design. A validator that skipped what it did not understand would
// accept {"pattern": "^[0-9]+$"}, tell the operator their payloads are checked,
// and then check nothing — and the first they would hear of it is a model being
// handed a value the schema said could not arrive.

// supportedKeywords is what CheckSchema will accept and validate() enforces.
// Adding a keyword here without implementing it below reintroduces exactly the
// silence this design exists to prevent.
var supportedKeywords = map[string]bool{
	"$schema":              true, // metadata, ignored
	"title":                true, // metadata, ignored
	"description":          true, // metadata, ignored
	"type":                 true,
	"enum":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"items":                true,
	"minItems":             true,
	"maxItems":             true,
	"minLength":            true,
	"maxLength":            true,
	"minimum":              true,
	"maximum":              true,
}

var supportedTypes = map[string]bool{
	"object": true, "array": true, "string": true,
	"number": true, "integer": true, "boolean": true, "null": true,
}

const (
	// MaxJSONPayload bounds a JSON task's body. A job description that does not
	// fit in a megabyte is a file, and a file task is how those are delivered —
	// the agent gets a path instead of a megabyte of text in its prompt.
	MaxJSONPayload = 1 << 20

	// MaxFilePayload bounds a file task's body. It is generous because the
	// bytes never reach a model, only the disk; it exists so one caller cannot
	// fill the workspace directory, and so an operator is told a number rather
	// than watching a request hang.
	MaxFilePayload = 32 << 20
)

// CheckSchema proves a task's schema is one this validator can actually
// enforce. It runs when the task is written, so the operator who typed the
// schema is the one who reads the complaint.
func CheckSchema(raw string) error {
	node, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("the task schema must be a JSON Schema object: %w", err)
	}
	return checkNode(node, "")
}

func decodeObject(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var node map[string]any
	if err := dec.Decode(&node); err != nil {
		return nil, err
	}
	if node == nil {
		return nil, fmt.Errorf("it is null")
	}
	return node, nil
}

func checkNode(node map[string]any, path string) error {
	for k := range node {
		if !supportedKeywords[k] {
			return fmt.Errorf("%s: %q is not one of the JSON Schema keywords this server enforces. "+
				"Supported: %s. A keyword that is stored but not checked would promise validation that never happens, so it is refused here instead",
				schemaAt(path), k, strings.Join(sortedKeys(supportedKeywords), ", "))
		}
	}

	if v, ok := node["type"]; ok {
		t, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: \"type\" must be a single type name (a list of types is not supported here)", schemaAt(path))
		}
		if !supportedTypes[t] {
			return fmt.Errorf("%s: %q is not a JSON Schema type. Use one of: %s",
				schemaAt(path), t, strings.Join(sortedKeys(supportedTypes), ", "))
		}
	}

	if v, ok := node["enum"]; ok {
		list, ok := v.([]any)
		if !ok || len(list) == 0 {
			return fmt.Errorf("%s: \"enum\" must be a non-empty array of the values that are allowed", schemaAt(path))
		}
	}

	if v, ok := node["properties"]; ok {
		props, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: \"properties\" must be an object mapping each field name to its schema", schemaAt(path))
		}
		for name, sub := range props {
			subNode, ok := sub.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: the schema for %q must be an object", schemaAt(path), name)
			}
			if err := checkNode(subNode, join(path, name)); err != nil {
				return err
			}
		}
	}

	if v, ok := node["required"]; ok {
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: \"required\" must be an array of field names", schemaAt(path))
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("%s: every entry in \"required\" must be a field name", schemaAt(path))
			}
		}
	}

	if v, ok := node["additionalProperties"]; ok {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: \"additionalProperties\" must be true or false here — a schema as its value is not supported", schemaAt(path))
		}
	}

	if v, ok := node["items"]; ok {
		sub, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: \"items\" must be a single schema object (per-position tuples are not supported here)", schemaAt(path))
		}
		if err := checkNode(sub, join(path, "[]")); err != nil {
			return err
		}
	}

	for _, k := range []string{"minItems", "maxItems", "minLength", "maxLength", "minimum", "maximum"} {
		if v, ok := node[k]; ok {
			if _, ok := v.(json.Number); !ok {
				return fmt.Errorf("%s: %q must be a number", schemaAt(path), k)
			}
		}
	}
	return nil
}

// ValidatePayload checks a delivered body against a task's schema. Its errors
// are what the caller gets in the 400, so they name the field and say what was
// expected rather than reporting that something, somewhere, was wrong.
func ValidatePayload(schema string, body []byte) error {
	if len(body) > MaxJSONPayload {
		return fmt.Errorf("the body is %d bytes and this task accepts at most %d — a payload this large belongs in a file task, where the agent is handed a path instead of the bytes",
			len(body), MaxJSONPayload)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("the body is empty; this task expects a JSON payload")
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("the body is not valid JSON: %w", err)
	}
	// A second document after the first means the caller sent something this
	// server would only half-read; saying so beats silently ignoring the rest.
	if dec.More() {
		return fmt.Errorf("the body has more than one JSON document in it; send exactly one")
	}

	node, err := decodeObject(schema)
	if err != nil {
		// The schema was checked when the task was written, so reaching this is
		// corruption of the row rather than anything the caller did.
		return fmt.Errorf("this task's stored schema is unreadable, so nothing can be checked against it: %w", err)
	}
	return validate(node, value, "")
}

func validate(schema map[string]any, v any, path string) error {
	if t, ok := schema["type"].(string); ok {
		if !matchesType(t, v) {
			return fmt.Errorf("%s: expected %s, got %s", at(path), article(t), describe(v))
		}
	}

	if raw, ok := schema["enum"]; ok {
		list, _ := raw.([]any)
		if !inEnum(list, v) {
			return fmt.Errorf("%s: %s is not one of the allowed values (%s)", at(path), render(v), renderList(list))
		}
	}

	switch typed := v.(type) {
	case string:
		if n, ok := numberOf(schema, "minLength"); ok && utf8.RuneCountInString(typed) < int(n) {
			return fmt.Errorf("%s: needs at least %s, got %d", at(path), count(int(n), "character"), utf8.RuneCountInString(typed))
		}
		if n, ok := numberOf(schema, "maxLength"); ok && utf8.RuneCountInString(typed) > int(n) {
			return fmt.Errorf("%s: allows at most %s, got %d", at(path), count(int(n), "character"), utf8.RuneCountInString(typed))
		}

	case json.Number:
		f, err := typed.Float64()
		if err != nil {
			return fmt.Errorf("%s: %q is not a number this server can compare", at(path), typed.String())
		}
		if n, ok := numberOf(schema, "minimum"); ok && f < n {
			return fmt.Errorf("%s: must be at least %s, got %s", at(path), trimNum(n), typed.String())
		}
		if n, ok := numberOf(schema, "maximum"); ok && f > n {
			return fmt.Errorf("%s: must be at most %s, got %s", at(path), trimNum(n), typed.String())
		}

	case map[string]any:
		if raw, ok := schema["required"]; ok {
			list, _ := raw.([]any)
			for _, item := range list {
				name, _ := item.(string)
				if _, present := typed[name]; !present {
					return fmt.Errorf("%s is required but was not sent", at(join(path, name)))
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		if allow, ok := schema["additionalProperties"].(bool); ok && !allow {
			for name := range typed {
				if _, declared := props[name]; !declared {
					return fmt.Errorf("%s: %q is not a field this task accepts (it allows only: %s)",
						at(path), name, strings.Join(sortedKeys(anyKeys(props)), ", "))
				}
			}
		}
		// Iteration is over the schema, not the payload, so the order a caller
		// happened to serialise their fields in cannot change which complaint
		// they get back for the same body.
		for _, name := range sortedKeys(anyKeys(props)) {
			child, present := typed[name]
			if !present {
				continue // absence is "required"'s business, checked above
			}
			sub, _ := props[name].(map[string]any)
			if err := validate(sub, child, join(path, name)); err != nil {
				return err
			}
		}

	case []any:
		if n, ok := numberOf(schema, "minItems"); ok && len(typed) < int(n) {
			return fmt.Errorf("%s: needs at least %s, got %d", at(path), count(int(n), "item"), len(typed))
		}
		if n, ok := numberOf(schema, "maxItems"); ok && len(typed) > int(n) {
			return fmt.Errorf("%s: allows at most %s, got %d", at(path), count(int(n), "item"), len(typed))
		}
		if sub, ok := schema["items"].(map[string]any); ok {
			for i, item := range typed {
				if err := validate(sub, item, fmt.Sprintf("%s[%d]", at(path), i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func matchesType(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		// Int64 is what decides it, so 3.0 and 3 are both integers and 3.5 is
		// not — and a value too large for int64 is refused rather than rounded
		// into something the agent would be told is a whole number.
		_, err := n.Int64()
		return err == nil
	}
	return false
}

func numberOf(schema map[string]any, key string) (float64, bool) {
	n, ok := schema[key].(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil {
		return 0, false
	}
	return f, true
}

// inEnum compares by canonical JSON rather than by Go equality, because a
// decoded payload holds json.Number where the schema holds json.Number too and
// neither is comparable with == across types.
func inEnum(list []any, v any) bool {
	want, err := json.Marshal(v)
	if err != nil {
		return false
	}
	for _, item := range list {
		got, err := json.Marshal(item)
		if err != nil {
			continue
		}
		if bytes.Equal(got, want) {
			return true
		}
	}
	return false
}

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return "something else"
}

func article(t string) string {
	switch t {
	case "object", "array", "integer":
		return "an " + t
	case "null":
		return "null"
	}
	return "a " + t
}

func render(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "that value"
	}
	return string(raw)
}

func renderList(list []any) string {
	parts := make([]string, 0, len(list))
	for _, item := range list {
		parts = append(parts, render(item))
	}
	return strings.Join(parts, ", ")
}

// count prints a bound as English. "needs at least 1 items" is the sort of
// thing a caller reads twice wondering whether it means something else.
func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// trimNum prints a bound the way the operator wrote it: 5 rather than 5.000000.
func trimNum(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// at names the place a complaint is about. The root has no field name, so it is
// called what the caller sent rather than being left blank.
func at(path string) string {
	if path == "" {
		return "the payload"
	}
	return path
}

// schemaAt is at() for complaints about the schema itself, which an operator
// reads while looking at the schema they just wrote.
func schemaAt(path string) string {
	if path == "" {
		return "the task schema"
	}
	return "the task schema at " + path
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func anyKeys(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// CheckContentType proves a file task's declared media type is one this server
// can compare a request against. An empty type accepts anything, which is a
// real choice for an inlet whose caller is trusted to send what was agreed.
func CheckContentType(ct string) error {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return nil
	}
	if strings.HasSuffix(ct, "/*") || ct == "*/*" {
		return nil
	}
	if _, _, err := mime.ParseMediaType(ct); err != nil {
		return fmt.Errorf("content type %q is not a media type: use something like \"image/png\", or \"image/*\" to accept any image: %w", ct, err)
	}
	return nil
}

// MatchesContentType reports whether what arrived is what the task accepts.
// Parameters are dropped before comparing: a caller sending
// "text/csv; charset=utf-8" sent a CSV, and refusing it over the charset would
// be pedantry the operator has to debug.
func MatchesContentType(accepts, got string) bool {
	accepts = strings.TrimSpace(strings.ToLower(accepts))
	if accepts == "" || accepts == "*/*" {
		return true
	}
	base, _, err := mime.ParseMediaType(got)
	if err != nil {
		base = strings.ToLower(strings.TrimSpace(strings.Split(got, ";")[0]))
	}
	base = strings.ToLower(base)
	if base == accepts {
		return true
	}
	if prefix, ok := strings.CutSuffix(accepts, "/*"); ok {
		major, _, found := strings.Cut(base, "/")
		return found && major == prefix
	}
	return false
}
