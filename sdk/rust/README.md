# Writing a Cogitorium plugin in Rust

A plugin is a WebAssembly module the server loads and calls into.

```rust
use cogitorium::{plugin, Host, Request, Result};
use serde_json::{json, Value};

fn home(r: &Request, h: &Host) -> Result<Value> {
    Ok(json!({
        "Greeting": format!("Hello, {}.", r.ctx.viewer.name),
        "Now": h.now()?,
    }))
}

plugin! { "myplugin", "home" => home }
```

```toml
# Cargo.toml
[lib]
# A cdylib, because the host loads this and calls in. A binary would run once
# and end, and a module that has ended has no exports left to call.
crate-type = ["cdylib"]

[dependencies]
cogitorium = "0.1"
serde_json = "1"

[profile.release]
opt-level = "z"
lto = true
strip = true
```

```yaml
# plugin.yaml
schema: 1
id: myplugin
name: My Plugin
version: 1.0.0
license: Apache-2.0
needs: rust
host:
  contract: 1
pages:
  - path: /p/myplugin/
    template: myplugin.page.home
    title: My Plugin
    provider: home
```

## Building

```bash
rustup target add wasm32-wasip1
cargo build --release --target wasm32-wasip1
cp target/wasm32-wasip1/release/myplugin.wasm plugin.wasm

cogitorium plugins build .
cogitorium plugins install myplugin.zip
cogitorium plugins approve myplugin
cogitorium plugins enable myplugin
```

The release profile above matters more here than it usually does: an operator
approving your plugin is approving a binary, and 124 KB is a different thing to
be handed than 2 MB.

## There is no main

The `plugin!` macro generates the three functions the host looks for. Your
exports are named to the macro rather than registered by code that runs at
startup, because nothing of yours runs at startup — the host loads the module
and calls straight in.

## What a plugin may ask the host for

Nine calls, identical in every SDK and on every tier. A plugin that started in
Python and was rewritten here calls the same nine things.

| call | what it is for |
| --- | --- |
| `h.log(msg)` | the server's log, tagged with your plugin |
| `h.now()` | the host's clock — pinnable, so your output is reproducible in a test |
| `h.rand(max)` | randomness, pinned the same way |
| `h.config()` | what the operator set for your plugin |
| `h.render(name, data)` | one of your templates, through the layer stack |
| `h.http(method, url, headers, body)` | outbound, only to hosts listed under `hosts:` |
| `h.api(method, path, body)` | this server's API, as your plugin, only the subjects under `api:` |
| `h.enqueue(export, args, after, key)` | your own export, later, on the durable queue |
| `h.get/set/delete/incr/keys/compare_and_set` | your own storage |

`incr` and `compare_and_set` exist because two instances of your plugin **will**
run at once. Read-then-write loses increments, and you find out from a wrong
number rather than from an error.

An `Err` from a host call is the host refusing — a host you were not granted, a
scope the operator did not approve. It carries the host's own sentence, which
already names both what you asked for and what you have.

## Testing without a server

The crate builds for your own machine too, so `cargo test` works on a plugin's
pure logic. Host calls have nowhere to go in that build and say so rather than
pretending to succeed.

## A working example

[`examples/plugins/rustsample`](../../examples/plugins/rustsample) — a page, the
host's clock, and a counter that survives the instance.
