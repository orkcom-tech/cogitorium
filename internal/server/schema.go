package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// jsonSchema describes a request body from the Go type the handler decodes
// into, so the description and the parser cannot disagree about what a field
// is called or what it holds.
//
// Reflection rather than annotations: a comment that has to be kept in step
// with the struct beside it is the drift this whole exercise exists to remove.
// The struct tags are already the contract — encoding/json reads them, and so
// does this.
func jsonSchema(v any) string {
	if v == nil {
		return ""
	}
	// Fourteen spaces, because that is where this lands: paths > path >
	// method > requestBody > content > media type > schema. Eight put the
	// schema's own keys beside `content` instead of under `schema`, which
	// is valid YAML and invalid OpenAPI — it parsed, and the schema read
	// back as null.
	return renderSchema(reflect.TypeOf(v), 14)
}

func renderSchema(t reflect.Type, indent int) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	pad := strings.Repeat(" ", indent)

	switch t.Kind() {
	case reflect.Struct:
		fields := structFields(t)
		if len(fields) == 0 {
			return pad + "type: object\n"
		}
		var b strings.Builder
		b.WriteString(pad + "type: object\n")
		b.WriteString(pad + "properties:\n")
		for _, f := range fields {
			b.WriteString(pad + "  " + quote(f.name) + ":\n")
			b.WriteString(renderSchema(f.typ, indent+4))
		}
		return b.String()
	case reflect.Slice, reflect.Array:
		// []byte is base64 in JSON, not an array of numbers.
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			return pad + "type: string\n" + pad + "contentEncoding: base64\n"
		}
		return pad + "type: array\n" + pad + "items:\n" + renderSchema(t.Elem(), indent+2)
	case reflect.Map:
		return pad + "type: object\n" + pad + "additionalProperties: true\n"
	case reflect.String:
		return pad + "type: string\n"
	case reflect.Bool:
		return pad + "type: boolean\n"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return pad + "type: integer\n"
	case reflect.Float32, reflect.Float64:
		return pad + "type: number\n"
	case reflect.Interface:
		// json.RawMessage and any: the field carries whatever the caller sends,
		// which is a true statement about it rather than a gap.
		return pad + "description: any JSON value\n"
	}
	return pad + "description: " + quote(t.String()) + "\n"
}

type schemaField struct {
	name string
	typ  reflect.Type
}

// structFields walks a struct the way encoding/json does — embedded structs
// flattened, `-` skipped, the tag's name winning — because a description that
// walked it differently would describe a body this server does not accept.
func structFields(t reflect.Type) []schemaField {
	var out []schemaField
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				out = append(out, structFields(ft)...)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		// json.RawMessage is a []byte with a meaning: anything at all.
		if f.Type == reflect.TypeOf(json.RawMessage(nil)) {
			out = append(out, schemaField{name: name, typ: reflect.TypeOf((*any)(nil)).Elem()})
			continue
		}
		out = append(out, schemaField{name: name, typ: f.Type})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// describedBodies counts how many mutating routes carry a body type, which is
// what the ratchet in the tests watches.
func describedBodies(routes []Route) (described, mutating int) {
	for _, r := range routes {
		if r.Method != "POST" && r.Method != "PUT" && r.Method != "PATCH" {
			continue
		}
		mutating++
		if r.Body != nil {
			described++
		}
	}
	return
}

func mutatingWithoutBody(routes []Route) []string {
	var out []string
	for _, r := range routes {
		if r.Method != "POST" && r.Method != "PUT" && r.Method != "PATCH" {
			continue
		}
		if r.Body == nil {
			out = append(out, fmt.Sprintf("%s %s", r.Method, r.Path))
		}
	}
	sort.Strings(out)
	return out
}
