package server

import (
	"fmt"
	"regexp"
	"strings"
)

// openAPI renders the route inventory as an OpenAPI 3.1 document.
//
// Written out rather than produced by a library, for the same reason the rest
// of this product has few dependencies: the output is a few hundred lines of
// YAML with a fixed shape, and a generator that has to be configured is more
// code than the thing it generates.
//
// What this document promises is exactly what the inventory knows — every path,
// its methods, its parameters and which credential opens it. It does not
// describe request and response BODIES yet, and it says so in its own
// description rather than leaving a reader to discover that the schemas are
// missing. A specification that quietly under-describes is worse than one that
// states its own edge.
func openAPI(routes []Route) string {
	var b strings.Builder
	b.WriteString(`openapi: 3.1.0
info:
  title: Cogitorium
  version: "1"
  description: |-
    The HTTP surface of a Cogitorium install, generated from the server's own
    route table by a test that fails when the two disagree — so this lists every
    endpoint that exists, and only those.

    Request bodies are described where the handler decodes into a named type,
    and those schemas are the parser's own definition rather than a second
    account of it. The rest are being named route by route; until then their
    shape is in the reference at https://orkcom-tech.github.io/cogitorium/.
    A test holds the count of undescribed ones so it can only fall.

    Response bodies are not described yet.

    The version above is the API surface — everything here lives under /api/v1 —
    and deliberately not the build's version: a document that changed with every
    release would report a new API on a day nothing about it moved, and would
    fail its own generation test on any build that stamps a version in.
  license:
    name: Apache-2.0
    identifier: Apache-2.0
servers:
  - url: http://127.0.0.1:8688
    description: the default listen address
components:
  securitySchemes:
    token:
      type: http
      scheme: bearer
      description: |-
        A user's token. On a loopback listen address a local request is treated
        as the admin without one; change the address and it becomes required.
    inletKey:
      type: http
      scheme: bearer
      description: |-
        A receiver's own key, which opens that receiver and nothing else. These
        are the only paths exempt from normal authentication.
security:
  - token: []
paths:
`)

	// Grouped by path so each appears once with its methods beneath it, which
	// is what the format wants and also how anybody reads it.
	for _, path := range distinctPaths(routes) {
		b.WriteString("  " + quote(path) + ":\n")
		if params := pathParams(path); params != "" {
			b.WriteString(params)
		}
		for _, r := range routes {
			if r.Path != path {
				continue
			}
			b.WriteString("    " + strings.ToLower(r.Method) + ":\n")
			b.WriteString("      summary: " + quote(summarise(r)) + "\n")
			switch r.Auth {
			case AuthNone:
				b.WriteString("      security: []\n")
			case AuthInletKey:
				b.WriteString("      security:\n        - inletKey: []\n")
			}
			if schema := jsonSchema(r.Body); schema != "" {
				b.WriteString("      requestBody:\n        required: true\n        content:\n")
				b.WriteString("          application/json:\n            schema:\n")
				b.WriteString(schema)
			}
			b.WriteString("      responses:\n        default:\n          description: see the reference\n")
		}
	}
	return b.String()
}

func distinctPaths(routes []Route) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range routes {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, r.Path)
		}
	}
	return out
}

var paramRe = regexp.MustCompile(`\{([^}]+)\}`)

// pathParams declares each {placeholder} once for the whole path, which is
// where OpenAPI expects parameters shared by every method on it.
func pathParams(path string) string {
	names := paramRe.FindAllStringSubmatch(path, -1)
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("    parameters:\n")
	for _, m := range names {
		name := strings.TrimSuffix(m[1], "...")
		b.WriteString("      - name: " + quote(name) + "\n")
		b.WriteString("        in: path\n        required: true\n")
		// Every placeholder in this server is either a numeric id or a name;
		// saying "string" for an id would be wrong in the other direction.
		if name == "id" {
			b.WriteString("        schema:\n          type: integer\n          format: int64\n")
		} else {
			b.WriteString("        schema:\n          type: string\n")
		}
	}
	return b.String()
}

// summarise says what an endpoint is for, from its own shape. Derived rather
// than written down, so a new route arrives with a sentence instead of with a
// gap somebody has to notice.
func summarise(r Route) string {
	verb := map[string]string{
		"GET": "Read", "POST": "Create", "PUT": "Replace",
		"PATCH": "Update", "DELETE": "Delete",
	}[r.Method]
	if verb == "" {
		verb = r.Method
	}
	subject := strings.TrimPrefix(r.Path, "/api/v1/")
	subject = strings.TrimPrefix(subject, "/")
	if r.Auth == AuthInletKey {
		return "Deliver to a receiver, or read a run it produced"
	}
	if r.Path == "/health" {
		return "Liveness, without authentication"
	}
	return fmt.Sprintf("%s %s", verb, subject)
}

// quote emits a YAML string that survives every character these paths contain.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
