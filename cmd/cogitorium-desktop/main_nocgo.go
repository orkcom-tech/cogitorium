//go:build !cgo

// The desktop shell needs a system webview, and a system webview needs cgo.
//
// This file exists so that `go build ./...` with CGO_ENABLED=0 still works —
// which is not a detail: the server's release cross-builds six targets from
// one machine with cgo off, and CI compiles the whole module that way on every
// change. Without a file that survives the constraint, the package would have
// no Go files at all and the build would fail with a message about build
// constraints rather than about the desktop shell.
//
// So a build without cgo produces a binary that says exactly what it is.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr,
		"This build of the desktop shell was compiled without cgo, so it has no window.\n"+
			"The desktop application needs a system webview: WebKit on macOS, WebKitGTK on\n"+
			"Linux, WebView2 on Windows. Rebuild with CGO_ENABLED=1 (make desktop), or run\n"+
			"the server and use a browser:\n\n    cogitorium serve\n")
	os.Exit(1)
}
