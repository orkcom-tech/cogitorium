//! A plugin in Rust, on the WebAssembly tier.
//!
//! Build it with:
//!
//!     cargo build --release --target wasm32-wasip1
//!     cp target/wasm32-wasip1/release/rustsample.wasm plugin.wasm

use cogitorium::{plugin, Host, Request, Result};
use serde_json::{json, Value};

fn home(r: &Request, h: &Host) -> Result<Value> {
    // The host's clock rather than this module's, so `plugins invoke` can pin
    // it and this page can be asserted on in a test.
    let now = h.now()?;

    // Storage, to show what survives an instance: a module is instantiated per
    // call, so a counter kept in a static would read 1 forever.
    let visits = h.incr("visits", 1)?;

    let who = if r.ctx.viewer.signed_in { r.ctx.viewer.name.as_str() } else { "stranger" };

    Ok(json!({
        "Greeting": format!("Hello, {who}."),
        "Now": now,
        "Visits": visits,
    }))
}

plugin! { "rustsample", "home" => home }
