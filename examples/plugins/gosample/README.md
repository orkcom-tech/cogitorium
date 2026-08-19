# Go Sample

A plugin written against the [Go SDK](../../../sdk/go), on the WebAssembly tier.
It shows a page, reads the host's clock, and keeps a counter — the counter being
the point: a module is instantiated per call, so anything kept in a package
variable would read 1 forever.

Build it with either toolchain. The source is the same either way.

```bash
cd src
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o ../plugin.wasm .
```

```bash
cd src
tinygo build -target=wasip1 -buildmode=c-shared -o ../plugin.wasm .
```

Then, from the repository root:

```bash
cogitorium plugins build examples/plugins/gosample
cogitorium plugins install gosample.zip
cogitorium plugins approve gosample
cogitorium plugins enable gosample
```

and restart. The page is at `/p/gosample/`.
