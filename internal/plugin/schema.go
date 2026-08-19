package plugin

import (
	"encoding/json"
	"sort"
)

// The published schema for plugin.yaml.
//
// Generated from this package's own vocabularies rather than written out
// beside them, and checked by a test that fails when the committed file
// disagrees. A schema maintained by hand is a schema that is wrong the first
// time somebody adds a field — and being wrong here is worse than being
// absent, because an author's editor would then reject a manifest the server
// accepts, or accept one it refuses.
//
// It describes shape and closed vocabularies only. Everything cross-cutting —
// that a page's template is one the plugin ships, that a mount points at one of
// its own pages, that a native row matches this machine — stays in Validate,
// where it can say something useful. JSON Schema can say "must match one of"
// and cannot say "must be one of the templates in this bundle".

// SchemaURL is where the published copy lives. Written into the document so an
// editor pointed at a manifest can find it.
const SchemaURL = "https://orkcom-tech.github.io/cogitorium/plugin.schema.json"

// JSONSchema returns the schema as a decoded document, ready to marshal.
func JSONSchema() map[string]any {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	arrayOf := func(items any, desc string) map[string]any {
		return map[string]any{"type": "array", "items": items, "description": desc}
	}
	object := func(desc string, required []string, props map[string]any) map[string]any {
		o := map[string]any{
			"type": "object", "description": desc,
			"properties":           props,
			"additionalProperties": false,
		}
		if len(required) > 0 {
			o["required"] = required
		}
		return o
	}

	return map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         SchemaURL,
		"title":       "Cogitorium plugin manifest",
		"description": "plugin.yaml. Shape and closed vocabularies; the server checks the rest and says why.",
		"type":        "object",
		"required":    []string{"schema", "id", "name", "version", "host"},
		// Closed, because a field nobody implements is a field an author
		// believes in. A typo in an optional key is otherwise silence.
		"additionalProperties": false,
		"properties": map[string]any{
			"schema": map[string]any{
				"const":       1,
				"description": "The manifest format. One value today; a second would mean a second reader.",
			},
			"id": map[string]any{
				"type": "string", "pattern": `^[a-z][a-z0-9-]{2,47}$`,
				"not":         map[string]any{"enum": reservedIDList()},
				"description": "3–48 lowercase characters. Becomes a template namespace and a URL prefix, so it is not free-form.",
			},
			"name":    str("What a person reads on the plugins screen."),
			"version": map[string]any{"type": "string", "pattern": `^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`, "description": "Semantic version. Approval is bound to content, not to this — but an update is detected by it."},
			"license": str("SPDX identifier."),
			"docs":    map[string]any{"type": "string", "format": "uri"},
			"source":  map[string]any{"type": "string", "format": "uri", "description": "Where somebody reads the code before approving it."},

			"host": object("What this plugin needs of the host.", []string{"contract"}, map[string]any{
				"contract":   map[string]any{"type": "integer", "minimum": 1, "description": "The ABI generation this was written against."},
				"cogitorium": str("A version range, when this needs one. Absent means any."),
			}),

			"needs": map[string]any{
				"type": "string", "enum": allTechnologies(),
				"description": "The technology you wrote it in. You declare this; the host decides the tier, and which tier it picked is never your problem.",
			},

			"pages": arrayOf(object("A page this plugin serves under /p/.",
				[]string{"path", "template"}, map[string]any{
					"path":     map[string]any{"type": "string", "pattern": `^/p/`, "description": "Must live under /p/<id>/."},
					"template": str("A template this plugin ships."),
					"title":    str(""),
					"provider": str("An export of this plugin's own code supplying the page's model."),
					"auth": map[string]any{
						"enum":        []string{"token", "admin", "none"},
						"default":     AuthDefault,
						"description": "Defaults to token. \"none\" is allowed and is shown in red on the approval screen.",
					},
				}), "Pages, served by the host at the plugin's own URLs."),

			"nav": arrayOf(object("An entry on the rail.", []string{"area", "label", "href"}, map[string]any{
				"area":  map[string]any{"enum": []string{"rail"}},
				"label": str(""),
				"icon":  str(""),
				"href":  map[string]any{"type": "string", "pattern": `^/`, "description": "An absolute path."},
				"order": map[string]any{"type": "integer"},
				"when":  map[string]any{"enum": []string{"always", "workspace", "admin"}, "default": "always"},
			}), "Rail entries."),

			"mounts": arrayOf(object("A panel inside one of the product's own screens.",
				[]string{"point", "title", "page"}, map[string]any{
					"point": map[string]any{"enum": mountPointList(), "description": "The only mount points that exist. An unknown one is refused at install rather than ignored."},
					"title": str(""),
					"icon":  str(""),
					"page":  str("One of this plugin's own pages. A mount opening somebody else's page is refused."),
				}), "Where this plugin hangs a panel."),

			"styles":  arrayOf(map[string]any{"type": "string"}, "Stylesheets, injected into every screen after the product's own."),
			"scripts": arrayOf(object("A module injected into every screen.", []string{"src"}, map[string]any{
				"src":  str(""),
				"type": map[string]any{"enum": []string{"module"}, "default": "module"},
			}), "Modules."),

			"overrides": arrayOf(map[string]any{"type": "string"},
				"Template names you mean to take over. Advisory: the host computes what you actually override from what you ship, and declaring earns nothing. It is here so the approval screen can show intent beside effect."),

			"native": arrayOf(object("One prebuilt binary and the machine it is for.",
				[]string{"os", "arch", "path"}, map[string]any{
					"os":   map[string]any{"enum": []string{"linux", "darwin", "windows"}},
					"arch": map[string]any{"enum": []string{"amd64", "arm64"}},
					"libc": map[string]any{"enum": []string{"glibc", "musl"}, "description": "Linux only, and only when it matters."},
					"path": str("Inside the bundle."),
				}), "Only for needs: native."),

			"hosts": arrayOf(map[string]any{"type": "string"},
				"Hosts this asks to reach. A grant an operator sees before approving, never a permission this file confers. Wildcards as *.example.com; ports are refused."),
			"secrets": arrayOf(map[string]any{"type": "string"}, "Named values this asks for."),
			"api":     arrayOf(map[string]any{"type": "string"}, "Endpoints of this server this asks to call."),
		},
	}
}

// JSONSchemaBytes is the schema as it is published: stable key order, indented,
// newline-terminated, so a diff is about content rather than formatting.
func JSONSchemaBytes() ([]byte, error) {
	b, err := json.MarshalIndent(JSONSchema(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func allTechnologies() []string {
	out := make([]string, 0, len(technologies))
	for name := range technologies {
		// Superseded names included: a published plugin naming one still
		// installs, and an editor marking it invalid would be telling an
		// author their working manifest is broken.
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func reservedIDList() []string {
	out := make([]string, 0, len(reservedIDs))
	for id := range reservedIDs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func mountPointList() []string {
	out := make([]string, 0, len(mountPoints))
	for p := range mountPoints {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
