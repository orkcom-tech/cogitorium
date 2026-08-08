package engine

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// toolArgs decodes tool-call arguments field by field. Models — small ones
// especially — routinely send a wrong type for one field; decoding the whole
// payload into a struct would throw away the fields they got right and hand
// back a Go-internals error the model cannot act on. Each accessor instead
// reports precisely which argument was wrong and what was expected, which is
// what lets the model correct itself on the next iteration.
type toolArgs struct {
	tool   string
	fields map[string]json.RawMessage
}

func parseArgs(tool, input string) (toolArgs, error) {
	a := toolArgs{tool: tool, fields: map[string]json.RawMessage{}}
	if input == "" {
		return a, nil
	}
	if err := json.Unmarshal([]byte(input), &a.fields); err != nil {
		return a, fmt.Errorf("tool %s: arguments must be a JSON object: %w", tool, err)
	}
	return a, nil
}

func (a toolArgs) has(key string) bool {
	_, ok := a.fields[key]
	return ok
}

// str returns a string argument. Numbers and booleans are read as their
// literal text; structured values are rejected with an actionable message.
func (a toolArgs) str(key string) (string, error) {
	raw, ok := a.fields[key]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return strconv.FormatBool(b), nil
	}
	return "", fmt.Errorf("tool %s: argument %q must be a string, got %s", a.tool, key, string(raw))
}

// reqStr is str plus a presence check.
func (a toolArgs) reqStr(key string) (string, error) {
	s, err := a.str(key)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("tool %s: argument %q is required", a.tool, key)
	}
	return s, nil
}

func (a toolArgs) strSlice(key string) ([]string, error) {
	raw, ok := a.fields[key]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err == nil {
		return out, nil
	}
	// A single string where a list was expected is unambiguous.
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}
	return nil, fmt.Errorf("tool %s: argument %q must be an array of strings, got %s", a.tool, key, string(raw))
}

// jsonString returns an argument that is itself JSON: accepted either as a
// JSON string containing JSON, or as an inline object.
func (a toolArgs) jsonString(key string) (string, error) {
	raw, ok := a.fields[key]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	return string(raw), nil
}

func (a toolArgs) decode(key string, target any) error {
	raw, ok := a.fields[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("tool %s: argument %q has the wrong shape: %w", a.tool, key, err)
	}
	return nil
}
