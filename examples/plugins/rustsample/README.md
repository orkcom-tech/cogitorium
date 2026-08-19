# Rust Sample

A plugin written against the [Rust SDK](../../../sdk/rust), on the WebAssembly
tier. It shows a page, reads the host's clock, and keeps a counter — the counter
being the point: a module is instantiated per call, so anything kept in a static
would read 1 forever.

```bash
rustup target add wasm32-wasip1
cargo build --release --target wasm32-wasip1
cp target/wasm32-wasip1/release/rustsample.wasm plugin.wasm
```

Then, from the repository root:

```bash
cogitorium plugins build examples/plugins/rustsample
cogitorium plugins install rustsample.zip
cogitorium plugins approve rustsample
cogitorium plugins enable rustsample
```

and restart. The page is at `/p/rustsample/`.
