// Its own module, on purpose.
//
// This compiles to a WebAssembly artifact the server embeds — the server never
// imports it. Keeping it out of the main module's graph means a JavaScript
// engine does not become a dependency of a binary that mostly does not run
// JavaScript, and `go list -m all` for the product keeps answering the
// question it answers today.
module cogitorium.js-engine

go 1.25

require github.com/dop251/goja v0.0.0-20250630131328-58d95d85e994

require (
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	golang.org/x/text v0.3.8 // indirect
)
