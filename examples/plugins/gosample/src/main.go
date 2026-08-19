// A plugin in Go, on the WebAssembly tier.
//
// Build it with either toolchain — the source is the same either way:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../plugin.wasm .
//	tinygo build -target=wasip1 -buildmode=c-shared -o ../plugin.wasm .
//
// c-shared in both, because that is what produces a module the host can call
// into more than once. The default builds a command: it runs, it ends, and a
// module that has ended has no exports left to call.
package main

import (
	"fmt"

	cogitorium "github.com/orkcom-tech/cogitorium/sdk/go"
)

// At package level, because the host never runs main on this tier.
var plugin = cogitorium.New("gosample").Provider("home", home)

func home(r *cogitorium.Request, h *cogitorium.Host) (any, error) {
	// The host's clock rather than this module's, so `plugins invoke` can pin
	// it and this page can be asserted on in a test.
	now, err := h.Now()
	if err != nil {
		return nil, err
	}

	// Storage, to show what survives an instance: a module is instantiated per
	// call, so a counter kept in a package variable would read 1 forever.
	visits, err := h.Incr("visits", 1)
	if err != nil {
		return nil, err
	}

	who := r.Ctx.Viewer.Name
	if !r.Ctx.Viewer.SignedIn {
		who = "stranger"
	}

	return map[string]any{
		"Greeting": fmt.Sprintf("Hello, %s.", who),
		"Now":      now,
		"Visits":   visits,
	}, nil
}

func main() { plugin.Run() }
