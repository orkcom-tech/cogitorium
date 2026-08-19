// The JavaScript engine this binary carries.
//
// `needs: js` resolved to the WebAssembly tier and there was no engine behind
// it, so an author who declared it got a lane with no road: their plugin was
// asked for a .wasm they had never compiled, and nothing said why. This is the
// road.
//
// The module runs INSIDE wazero rather than in the server process, which is
// the point rather than an implementation detail. `needs: js` says the
// WebAssembly tier, and the isolation an author gets has to be the one the
// label promises — an interpreter embedded in the host would run a stranger's
// JavaScript on the server's own heap under a word that says sandbox.
//
// Built from source by the toolchain this project already uses, and
// reproducible with one command (see generate.sh). A QuickJS build would be
// roughly ten times smaller and considerably faster; it would also be an
// opaque binary in a repository whose whole argument is that you read what you
// approve. If a release pipeline ever grows wasi-sdk, replacing this file is a
// drop-in — the contract belongs to the module, not to the engine inside it.
package jsrt

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"sync"
)

//go:embed engine.wasm.gz
var compressed []byte

var (
	once   sync.Once
	module []byte
	err    error
)

// Module returns the engine, decompressed.
//
// Lazily and once: an install that runs no JavaScript plugin never pays the
// memory, and one that runs six pays it a single time. Stored compressed
// because the difference is 17MB against 3.7MB in every artifact this project
// ships to six targets, and the decompression is milliseconds on a path that
// happens at most once per boot.
func Module() ([]byte, error) {
	once.Do(func() {
		r, e := gzip.NewReader(bytes.NewReader(compressed))
		if e != nil {
			err = fmt.Errorf("jsrt: the embedded JavaScript engine is unreadable: %w", e)
			return
		}
		defer r.Close()
		module, err = io.ReadAll(r)
		if err != nil {
			err = fmt.Errorf("jsrt: the embedded JavaScript engine is truncated: %w", err)
		}
	})
	return module, err
}

// LoadExport is the name the engine answers on to receive a plugin's source.
//
// An export of the ENGINE, not of the plugin contract: a JavaScript plugin
// ships plugin.js and exports nothing, which is what makes the tier worth
// having. The host writes the source here before the first call on an
// instance.
const LoadExport = "cog_js_load"

// SourceFile is what a JavaScript plugin ships, by the same convention every
// other technology follows.
const SourceFile = "plugin.js"
