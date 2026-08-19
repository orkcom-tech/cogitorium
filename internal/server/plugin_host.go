package server

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/abi"
	"github.com/orkcom-tech/cogitorium/internal/plugin"
)

// The cog.* gateway.
//
// Every tier offers this identical set with identical semantics, which is the
// promise that lets an author outgrow one runtime and move to another by
// changing their build command and nothing else. A call answered on one tier
// and refused on another would quietly make the tier the author's problem,
// which is the one thing the tier model exists to prevent — so these are
// implemented here, above every runtime, rather than inside any of them.
//
// Every refusal is a value rather than a trap. A denied host or a key that is
// not there is an ordinary thing a plugin handles; trapping would turn "you
// may not reach that" into a crash with no message.

// clock and entropy are fields rather than direct calls to time.Now and
// crypto/rand for one reason: `plugins invoke` pins them, so a plugin's output
// is reproducible in a test. A guest reading its own clock cannot be.
type hostGateway struct {
	grants map[string]plugin.Grants
	db     *sql.DB
	// rt is the composed layer stack, so a plugin rendering a fragment goes
	// through the same machinery a page does — including other plugins'
	// overrides of the name it asked for.
	rt *pluginRuntime
	// config is what the operator set for each plugin, read-only. A plugin
	// writing its own configuration would be a plugin granting itself
	// something; what it wants to remember goes in kv.
	config map[string]map[string]any

	now  func() time.Time
	rand func(max int64) int64
}

func newHostGateway(grants map[string]plugin.Grants, db *sql.DB, rt *pluginRuntime,
	cfg map[string]map[string]any) *hostGateway {
	return &hostGateway{
		grants: grants, db: db, rt: rt, config: cfg,
		now: time.Now,
		rand: func(max int64) int64 {
			n, err := rand.Int(rand.Reader, big.NewInt(max))
			if err != nil {
				// crypto/rand failing is not a condition a plugin can do
				// anything about, and returning a predictable number quietly
				// would be worse than a refusal it can see.
				return -1
			}
			return n.Int64()
		},
	}
}

func (g *hostGateway) Call(id string, req abi.HostRequest) abi.HostReply {
	gr, known := g.grants[id]
	if !known {
		return abi.HostReply{Err: fmt.Sprintf("plugin %q has no grants recorded on this install", id)}
	}

	switch req.Call {
	case abi.CallLog:
		// Tagged with the plugin, so a line in the server's log is always
		// attributable to whoever wrote it.
		slog.Info("plugin", "plugin", id, "message", string(req.Input))
		return abi.HostReply{}

	case abi.CallNow:
		return reply(map[string]any{"unix_ms": g.now().UTC().UnixMilli(),
			"rfc3339": g.now().UTC().Format(time.RFC3339Nano)})

	case abi.CallRand:
		var in struct {
			Max int64 `json:"max"`
		}
		_ = json.Unmarshal(req.Input, &in)
		if in.Max <= 0 {
			return abi.HostReply{Err: "rand needs a positive max"}
		}
		n := g.rand(in.Max)
		if n < 0 {
			return abi.HostReply{Err: "this machine's randomness is unavailable"}
		}
		return reply(map[string]any{"n": n})

	case abi.CallConfig:
		// Absent is an empty object, not a refusal: a plugin with no operator
		// configuration is the ordinary case, and making it an error would
		// mean every plugin handling it.
		return reply(g.config[id])

	case abi.CallRender:
		return g.render(id, req.Input)

	case abi.CallKV:
		return g.kv(id, req.Input)

	case abi.CallHTTP:
		var in struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(req.Input, &in)
		if err := gr.AllowHost(hostOf(in.URL)); err != nil {
			return abi.HostReply{Err: err.Error()}
		}
		// The grant check is the part that is wired. Carrying the request
		// belongs with the gate that already substitutes credentials at the
		// edge, and doing it here would be a second way out of this process
		// with different rules.
		return abi.HostReply{Err: "outbound requests are not carried yet on this tier"}
	}
	return abi.HostReply{Err: fmt.Sprintf("%q is not answered yet on this tier", req.Call)}
}

// render runs a template through the layer stack.
//
// Through the stack, not against the plugin's own files: a plugin asking for a
// name another plugin has overridden gets the override, which is the whole
// point of the stack and would be silently wrong if this rendered in isolation.
func (g *hostGateway) render(id string, input json.RawMessage) abi.HostReply {
	var in struct {
		Template string          `json:"template"`
		Data     json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return abi.HostReply{Err: "render needs a template name"}
	}
	if g.rt == nil || g.rt.set == nil {
		return abi.HostReply{Err: "no templates are composed on this install"}
	}

	name, err := plugin.ParseName(in.Template)
	if err != nil {
		return abi.HostReply{Err: err.Error()}
	}
	// Its own namespace, or the host's. Rendering another plugin's private
	// name would let one plugin reach into another's internals through a
	// string, which is not a boundary anybody declared.
	if name.Namespace != id && name.Namespace != plugin.CoreNamespace {
		return abi.HostReply{Err: fmt.Sprintf(
			"%s belongs to %q, and a plugin may render its own names or the host's",
			in.Template, name.Namespace)}
	}

	var model any
	if len(in.Data) > 0 {
		if err := json.Unmarshal(in.Data, &model); err != nil {
			return abi.HostReply{Err: "the data is not readable: " + err.Error()}
		}
	}
	var out bytes.Buffer
	if err := g.rt.set.Execute(&out, in.Template, model); err != nil {
		return abi.HostReply{Err: err.Error()}
	}
	return reply(map[string]any{"html": out.String()})
}

// kv is a plugin's own durable storage: get, set, delete, list, cas and incr.
//
// Namespaced by plugin id in the primary key rather than by a prefix somebody
// has to remember to write, so one plugin cannot read another's rows even by
// constructing a key that looks like theirs.
func (g *hostGateway) kv(id string, input json.RawMessage) abi.HostReply {
	var in struct {
		Op      string `json:"op"`
		Key     string `json:"key"`
		Value   []byte `json:"value"`
		Version int64  `json:"version"`
		By      int64  `json:"by"`
		Prefix  string `json:"prefix"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return abi.HostReply{Err: "kv needs an op"}
	}
	if g.db == nil {
		return abi.HostReply{Err: "this install has no storage for plugins"}
	}
	if in.Op != "list" && in.Key == "" {
		return abi.HostReply{Err: "kv needs a key"}
	}
	if len(in.Value) > maxKVValue {
		return abi.HostReply{Err: fmt.Sprintf("a value may be %d bytes and this is %d",
			maxKVValue, len(in.Value))}
	}
	now := g.now().UTC().Format(time.RFC3339Nano)

	switch in.Op {
	case "get":
		var v []byte
		var version int64
		err := g.db.QueryRow(`SELECT value, version FROM plugin_kv WHERE plugin = ? AND key = ?`,
			id, in.Key).Scan(&v, &version)
		if err == sql.ErrNoRows {
			// Absent is a value, not an error. "There is nothing here" is
			// something a plugin asks about on purpose.
			return reply(map[string]any{"found": false})
		}
		if err != nil {
			return abi.HostReply{Err: err.Error()}
		}
		return reply(map[string]any{"found": true, "value": v, "version": version})

	case "set":
		_, err := g.db.Exec(`
			INSERT INTO plugin_kv (plugin, key, value, version, updated_at)
			VALUES (?, ?, ?, 1, ?)
			ON CONFLICT (plugin, key) DO UPDATE
			SET value = excluded.value, version = plugin_kv.version + 1, updated_at = excluded.updated_at`,
			id, in.Key, in.Value, now)
		if err != nil {
			return abi.HostReply{Err: err.Error()}
		}
		return reply(map[string]any{"ok": true})

	case "cas":
		// Compare on version rather than on the stored bytes: two writers who
		// happen to write identical values are two writers, and a value-based
		// compare cannot tell them apart — which is the exact case this is for.
		res, err := g.db.Exec(`
			UPDATE plugin_kv SET value = ?, version = version + 1, updated_at = ?
			WHERE plugin = ? AND key = ? AND version = ?`,
			in.Value, now, id, in.Key, in.Version)
		if err != nil {
			return abi.HostReply{Err: err.Error()}
		}
		n, _ := res.RowsAffected()
		if n == 0 && in.Version == 0 {
			// Version zero means "only if it does not exist yet".
			_, err := g.db.Exec(`INSERT OR IGNORE INTO plugin_kv
				(plugin, key, value, version, updated_at) VALUES (?, ?, ?, 1, ?)`,
				id, in.Key, in.Value, now)
			if err != nil {
				return abi.HostReply{Err: err.Error()}
			}
			var version int64
			_ = g.db.QueryRow(`SELECT version FROM plugin_kv WHERE plugin = ? AND key = ?`,
				id, in.Key).Scan(&version)
			return reply(map[string]any{"swapped": version == 1, "version": version})
		}
		return reply(map[string]any{"swapped": n > 0})

	case "incr":
		// A counter, in one statement, so two plugins incrementing at once do
		// not read-modify-write over each other.
		if in.By == 0 {
			in.By = 1
		}
		_, err := g.db.Exec(`
			INSERT INTO plugin_kv (plugin, key, value, version, updated_at)
			VALUES (?, ?, CAST(? AS BLOB), 1, ?)
			ON CONFLICT (plugin, key) DO UPDATE
			SET value = CAST(CAST(plugin_kv.value AS INTEGER) + ? AS BLOB),
			    version = plugin_kv.version + 1, updated_at = excluded.updated_at`,
			id, in.Key, fmt.Sprint(in.By), now, in.By)
		if err != nil {
			return abi.HostReply{Err: err.Error()}
		}
		var v []byte
		_ = g.db.QueryRow(`SELECT value FROM plugin_kv WHERE plugin = ? AND key = ?`,
			id, in.Key).Scan(&v)
		return reply(map[string]any{"value": string(v)})

	case "delete":
		if _, err := g.db.Exec(`DELETE FROM plugin_kv WHERE plugin = ? AND key = ?`,
			id, in.Key); err != nil {
			return abi.HostReply{Err: err.Error()}
		}
		return reply(map[string]any{"ok": true})

	case "list":
		rows, err := g.db.Query(`SELECT key, version FROM plugin_kv
			WHERE plugin = ? AND key LIKE ? ORDER BY key LIMIT ?`,
			id, in.Prefix+"%", maxKVKeys)
		if err != nil {
			return abi.HostReply{Err: err.Error()}
		}
		defer rows.Close()
		type row struct {
			Key     string `json:"key"`
			Version int64  `json:"version"`
		}
		out := []row{}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.Key, &r.Version); err != nil {
				return abi.HostReply{Err: err.Error()}
			}
			out = append(out, r)
		}
		// Said out loud rather than silently truncated: a plugin paging
		// through its own keys has to be able to tell it did not see them all.
		return reply(map[string]any{"keys": out, "truncated": len(out) == maxKVKeys})
	}
	return abi.HostReply{Err: fmt.Sprintf("kv has no operation %q", in.Op)}
}

const (
	// A ceiling, not a policy. A plugin storing megabytes per key is doing
	// something the operator's SQLite file will feel, and a limit somebody
	// hits is better than a database nobody can back up.
	maxKVValue = 1 << 20
	maxKVKeys  = 1000
)

func reply(v any) abi.HostReply {
	b, err := json.Marshal(v)
	if err != nil {
		return abi.HostReply{Err: err.Error()}
	}
	return abi.HostReply{Output: b}
}
