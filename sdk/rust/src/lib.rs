//! Write a Cogitorium plugin in Rust.
//!
//! A plugin is a WebAssembly module the server loads and calls into:
//!
//! ```ignore
//! use cogitorium::{plugin, Host, Request, Result};
//! use serde_json::json;
//!
//! fn home(r: &Request, h: &Host) -> Result<serde_json::Value> {
//!     Ok(json!({ "greeting": format!("Hello, {}.", r.ctx.viewer.name), "now": h.now()? }))
//! }
//!
//! plugin! { "myplugin", "home" => home }
//! ```
//!
//! Build it with:
//!
//! ```text
//! cargo build --release --target wasm32-wasip1
//! ```
//!
//! and ship `target/wasm32-wasip1/release/<name>.wasm` as `plugin.wasm`, with
//! `needs: rust` in plugin.yaml. There is no main: the host loads the module
//! and calls in, which is why exports are declared to the macro rather than
//! registered by code that runs at startup.
//!
//! The nine host calls here are the same nine every other tier offers, with
//! the same names and the same behaviour. A plugin that outgrows one language
//! and is rewritten in another calls the same nine things.

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::Value;

/// The ABI generation this SDK speaks. The host checks it before the first
/// call and refuses a mismatch there, rather than discovering it halfway
/// through one.
pub const CONTRACT: u32 = 1;

/// The host refusing, in its own words — a host that was not granted, a scope
/// the operator did not approve.
///
/// An ordinary value rather than a panic: being told no is a thing a plugin
/// handles.
#[derive(Debug, Clone)]
pub struct Error(pub String);

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Error {}

impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error(e.to_string())
    }
}

impl From<String> for Error {
    fn from(s: String) -> Self {
        Error(s)
    }
}

impl From<&str> for Error {
    fn from(s: &str) -> Self {
        Error(s.to_string())
    }
}

pub type Result<T> = std::result::Result<T, Error>;

// ── what the host sends ───────────────────────────────────────────────────

/// Who is asking, reduced to what a plugin legitimately needs. No token and no
/// session: a plugin that wants to act as somebody uses its own scoped
/// credential.
#[derive(Debug, Default, Clone, Deserialize)]
pub struct Viewer {
    #[serde(default)]
    pub id: i64,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub is_admin: bool,
    #[serde(default)]
    pub signed_in: bool,
}

/// Who is asking and where. The same field names a template sees, on purpose:
/// an author who learned them in a template should not learn them twice.
#[derive(Debug, Default, Clone, Deserialize)]
pub struct Ctx {
    #[serde(default)]
    pub viewer: Viewer,
    #[serde(default)]
    pub workspace: i64,
    #[serde(default)]
    pub install_mode: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub locale: String,
}

/// The part of an HTTP request a route export may see. `header` is an
/// allowlist rather than the request's headers — never a cookie, never the
/// Authorization header.
#[derive(Debug, Default, Clone, Deserialize)]
pub struct HttpRequest {
    #[serde(default)]
    pub method: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub params: std::collections::BTreeMap<String, String>,
    #[serde(default)]
    pub query: std::collections::BTreeMap<String, String>,
    #[serde(default)]
    pub header: std::collections::BTreeMap<String, String>,
    #[serde(default)]
    pub body: Option<Value>,
}

/// One call from the host. One shape for every role, so a plugin that grows a
/// second kind of export does not grow a second kind of function.
#[derive(Debug, Default, Clone, Deserialize)]
pub struct Request {
    #[serde(default)]
    pub export: String,
    #[serde(default)]
    pub role: String,
    #[serde(default)]
    pub ctx: Ctx,
    #[serde(default)]
    pub http: Option<HttpRequest>,
    #[serde(default)]
    pub input: Option<Value>,
}

impl Request {
    /// A task's or a tool's arguments, decoded into your own type.
    pub fn args<T: DeserializeOwned>(&self) -> Result<T> {
        let raw = self.input.clone().unwrap_or(Value::Object(Default::default()));
        Ok(serde_json::from_value(raw)?)
    }
}

/// A raw body — a file, a redirect target, anything that is not a rendered
/// view.
#[derive(Debug, Clone, Serialize)]
pub struct Content {
    #[serde(rename = "type")]
    pub kind: String,
    /// Base64 in the wire format, which is what the host's JSON does with
    /// bytes.
    pub body: String,
}

/// What an export returns when it wants more than a model.
///
/// Most exports return a plain value and never touch this. Reach for it to
/// render a named template through the layer stack, to answer with a file, or
/// to set a status.
#[derive(Debug, Default, Clone, Serialize)]
pub struct Response {
    /// Renders through the host's layer stack with `model` as its data, so
    /// what this plugin produces can itself be overridden by a plugin layered
    /// above it. Emitting finished HTML instead would put the answer outside
    /// the mechanism the whole system runs on.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub template: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<Content>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status: Option<u16>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub header: Option<std::collections::BTreeMap<String, String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

impl Response {
    /// Render a named template through the layer stack.
    pub fn template(name: &str, model: Value) -> Self {
        Response { template: Some(name.to_string()), model: Some(model), ..Default::default() }
    }
}

/// What an export is. Return a value and it becomes the model; return a
/// [`Response`] to say more than that.
pub trait Export {
    fn call(&self, request: &Request, host: &Host) -> Result<Value>;
}

impl<F> Export for F
where
    F: Fn(&Request, &Host) -> Result<Value>,
{
    fn call(&self, request: &Request, host: &Host) -> Result<Value> {
        self(request, host)
    }
}

// ── what a plugin may ask the host for ────────────────────────────────────

/// The nine calls, identical on every tier.
pub struct Host {
    _private: (),
}

/// What an outbound request came back with.
#[derive(Debug, Clone)]
pub struct HttpResponse {
    pub status: u16,
    pub headers: std::collections::BTreeMap<String, String>,
    pub body: Vec<u8>,
}

/// What this server's own API answered.
#[derive(Debug, Clone)]
pub struct ApiResponse {
    pub status: u16,
    pub body: Vec<u8>,
}

impl Host {
    fn call(&self, name: &str, input: Value) -> Result<Value> {
        let body = serde_json::to_vec(&serde_json::json!({ "call": name, "input": input }))?;
        let raw = raw::ask(&body)?;
        let reply: Value = serde_json::from_slice(&raw)?;
        if let Some(err) = reply.get("error").and_then(|v| v.as_str()) {
            if !err.is_empty() {
                // The host's own sentence, which already names both what was
                // asked for and what was granted.
                return Err(Error(err.to_string()));
            }
        }
        Ok(reply.get("output").cloned().unwrap_or(Value::Null))
    }

    /// Write to the server's log, tagged with this plugin.
    pub fn log(&self, message: &str) -> Result<()> {
        self.call("log", Value::String(message.to_string()))?;
        Ok(())
    }

    /// The host's clock, as an RFC3339 string.
    ///
    /// The host's rather than this module's so that `cogitorium plugins
    /// invoke` can pin it: a plugin reading its own clock cannot be reproduced
    /// in a test.
    pub fn now(&self) -> Result<String> {
        let out = self.call("now", serde_json::json!({}))?;
        Ok(out.get("rfc3339").and_then(|v| v.as_str()).unwrap_or_default().to_string())
    }

    /// An integer in `[0, max)`. Pinnable, like [`Host::now`], and for the
    /// same reason.
    pub fn rand(&self, max: i64) -> Result<i64> {
        let out = self.call("rand", serde_json::json!({ "max": max }))?;
        Ok(out.get("n").and_then(|v| v.as_i64()).unwrap_or_default())
    }

    /// What the operator set for this plugin. Read-only, and often empty.
    pub fn config(&self) -> Result<Value> {
        self.call("config", serde_json::json!({}))
    }

    /// Render one of this plugin's templates through the layer stack.
    ///
    /// Through the stack, so another plugin's override of the same name is
    /// what comes back — which is the point. Rendering your own file in
    /// isolation would be quietly wrong in exactly the case the system exists
    /// for.
    pub fn render(&self, template: &str, data: Value) -> Result<String> {
        let out = self.call("render", serde_json::json!({ "template": template, "data": data }))?;
        Ok(out.get("html").and_then(|v| v.as_str()).unwrap_or_default().to_string())
    }

    /// One outbound request through the host's gate. Only hosts listed under
    /// `hosts:` in plugin.yaml; the refusal names both what you asked for and
    /// what you were granted.
    pub fn http(
        &self,
        method: &str,
        url: &str,
        headers: std::collections::BTreeMap<String, String>,
        body: &[u8],
    ) -> Result<HttpResponse> {
        let out = self.call(
            "http",
            serde_json::json!({
                "url": url,
                "method": if method.is_empty() { "GET" } else { method },
                "headers": headers,
                "body": b64::encode(body),
            }),
        )?;
        Ok(HttpResponse {
            status: out.get("status").and_then(|v| v.as_u64()).unwrap_or_default() as u16,
            headers: out
                .get("headers")
                .and_then(|v| serde_json::from_value(v.clone()).ok())
                .unwrap_or_default(),
            body: b64::decode(out.get("body").and_then(|v| v.as_str()).unwrap_or_default())?,
        })
    }

    /// Call this server's own API as this plugin, never as the operator. Only
    /// subjects listed under `api:` in plugin.yaml; a write grant implies the
    /// matching read.
    pub fn api(&self, method: &str, path: &str, body: Option<Value>) -> Result<ApiResponse> {
        let out = self.call(
            "api",
            serde_json::json!({
                "method": if method.is_empty() { "GET" } else { method },
                "path": path,
                "body": body,
            }),
        )?;
        Ok(ApiResponse {
            status: out.get("status").and_then(|v| v.as_u64()).unwrap_or_default() as u16,
            body: b64::decode(out.get("body").and_then(|v| v.as_str()).unwrap_or_default())?,
        })
    }

    /// Run one of your own exports later, on the host's durable queue.
    ///
    /// `key` makes it idempotent, so enqueuing on every start does not
    /// accumulate one task per restart. `after` is a delay in seconds.
    pub fn enqueue(&self, export: &str, args: Value, after: i64, key: &str) -> Result<()> {
        self.call(
            "enqueue",
            serde_json::json!({ "export": export, "args": args, "after": after, "key": key }),
        )?;
        Ok(())
    }

    /// The stored bytes, or `None`. Absent is a value, not an error.
    pub fn get(&self, key: &str) -> Result<Option<Vec<u8>>> {
        let out = self.call("kv", serde_json::json!({ "op": "get", "key": key }))?;
        if !out.get("found").and_then(|v| v.as_bool()).unwrap_or(false) {
            return Ok(None);
        }
        Ok(Some(b64::decode(out.get("value").and_then(|v| v.as_str()).unwrap_or_default())?))
    }

    pub fn set(&self, key: &str, value: &[u8]) -> Result<()> {
        self.call(
            "kv",
            serde_json::json!({ "op": "set", "key": key, "value": b64::encode(value) }),
        )?;
        Ok(())
    }

    /// Removing a key that was never there is not an error.
    pub fn delete(&self, key: &str) -> Result<()> {
        self.call("kv", serde_json::json!({ "op": "delete", "key": key }))?;
        Ok(())
    }

    /// Add to a counter in one statement, so two instances of a plugin cannot
    /// lose one of their increments to a read-modify-write race. They WILL
    /// race, and an author should not have to learn that from a wrong number.
    pub fn incr(&self, key: &str, by: i64) -> Result<i64> {
        let out = self.call("kv", serde_json::json!({ "op": "incr", "key": key, "by": by }))?;
        // The count comes back as a string. A JSON number would be a float on
        // the way through every language that has only one number type, and a
        // counter that silently stops being exact past 2^53 is worse than a
        // string.
        let raw = out.get("value").and_then(|v| v.as_str()).unwrap_or_default();
        raw.trim()
            .parse::<i64>()
            .map_err(|_| Error(format!("the counter at {key:?} does not hold a number: {raw:?}")))
    }

    pub fn keys(&self, prefix: &str) -> Result<Vec<String>> {
        let out = self.call("kv", serde_json::json!({ "op": "list", "prefix": prefix }))?;
        Ok(out
            .get("keys")
            .and_then(|v| v.as_array())
            .map(|rows| {
                rows.iter()
                    .filter_map(|row| row.get("key").and_then(|k| k.as_str()))
                    .map(|s| s.to_string())
                    .collect()
            })
            .unwrap_or_default())
    }

    /// Write only if the stored version is still the one you read.
    ///
    /// Version rather than value: two writers who happen to write identical
    /// bytes are still two writers, and comparing values cannot tell them
    /// apart. Pass version 0 to mean "only if it does not exist yet".
    pub fn compare_and_set(&self, key: &str, value: &[u8], version: i64) -> Result<bool> {
        let out = self.call(
            "kv",
            serde_json::json!({
                "op": "cas", "key": key, "version": version, "value": b64::encode(value),
            }),
        )?;
        Ok(out.get("swapped").and_then(|v| v.as_bool()).unwrap_or(false))
    }
}

// ── the wasm boundary ─────────────────────────────────────────────────────

/// Plumbing the macro generates against. Not something a plugin calls.
#[doc(hidden)]
pub mod raw {
    use super::{Error, Export, Host, Request, Result, Response};
    use serde_json::Value;

    #[cfg(target_family = "wasm")]
    #[link(wasm_import_module = "cog")]
    extern "C" {
        fn cog_host(ptr: u32, size: u32, out_ptr: u32) -> u32;
    }

    /// Hand the host a place to write.
    ///
    /// The buffer is leaked on purpose: the host writes into it after this
    /// returns, so freeing it would be a use-after-free the guest cannot see
    /// and the host cannot explain. The leak is bounded by design — the host
    /// instantiates a fresh module per call, so everything here dies with the
    /// instance a moment later.
    pub fn alloc(size: u32) -> u32 {
        let mut buf = Vec::<u8>::with_capacity(size as usize);
        let ptr = buf.as_mut_ptr() as u32;
        std::mem::forget(buf);
        ptr
    }

    /// # Safety
    /// `ptr` and `size` must name a region the host wrote.
    pub unsafe fn read(ptr: u32, size: u32) -> Vec<u8> {
        std::slice::from_raw_parts(ptr as *const u8, size as usize).to_vec()
    }

    /// Pack a pointer and a length into the single word a wasm function can
    /// return.
    pub fn emit(bytes: &[u8]) -> u64 {
        let ptr = alloc(bytes.len() as u32);
        unsafe {
            std::ptr::copy_nonoverlapping(bytes.as_ptr(), ptr as *mut u8, bytes.len());
        }
        ((ptr as u64) << 32) | bytes.len() as u64
    }

    #[cfg(target_family = "wasm")]
    pub fn ask(body: &[u8]) -> Result<Vec<u8>> {
        let inptr = alloc(body.len() as u32);
        unsafe { std::ptr::copy_nonoverlapping(body.as_ptr(), inptr as *mut u8, body.len()) };

        // One word for the reply's packed pointer and length, written by the
        // host. Non-zero means "there is a reply where I said", and a REFUSAL
        // is a reply. Zero is the only case that is not a conversation — the
        // host could not answer at all.
        let slot = alloc(8);
        let code = unsafe { cog_host(inptr, body.len() as u32, slot) };
        if code == 0 {
            return Err(Error("the host could not answer at all".into()));
        }
        let packed = unsafe { std::ptr::read(slot as *const u64) };
        let (ptr, size) = ((packed >> 32) as u32, packed as u32);
        if size == 0 {
            return Ok(b"{}".to_vec());
        }
        Ok(unsafe { read(ptr, size) })
    }

    /// Off wasm there is no host to ask. Present so `cargo test` and
    /// `cargo check` work on your own machine, where a plugin's pure logic is
    /// worth testing without a server.
    #[cfg(not(target_family = "wasm"))]
    pub fn ask(_body: &[u8]) -> Result<Vec<u8>> {
        Err(Error("this build has no host: a host call only works inside the server".into()))
    }

    /// Make the host handle the macro hands to an export.
    pub fn host() -> Host {
        Host { _private: () }
    }

    /// Run one request against a dispatch table and produce the reply bytes.
    ///
    /// Shared by every plugin rather than generated per plugin, so a fix here
    /// is a fix everywhere.
    pub fn dispatch(id: &str, raw: &[u8], table: &[(&str, &dyn Export)]) -> Vec<u8> {
        let request: Request = match serde_json::from_slice(raw) {
            Ok(r) => r,
            Err(e) => return refusal(format!("the request envelope is not readable: {e}")),
        };

        let found = table.iter().find(|(name, _)| *name == request.export);
        let Some((_, export)) = found else {
            // Named, with what does exist. An author whose export is never
            // called has otherwise no way to tell a typo from a host that
            // never asked.
            let names: Vec<&str> = table.iter().map(|(n, _)| *n).collect();
            let have = if names.is_empty() { "none".to_string() } else { names.join(", ") };
            return refusal(format!(
                "{id} has no export {:?}; it has: {have}",
                request.export
            ));
        };

        let host = host();
        match export.call(&request, &host) {
            Err(e) => refusal(e.0),
            Ok(value) => {
                // A returned Response is the author saying more than a model.
                // Recognised by shape rather than by a second trait, so an
                // export that answers both ways stays one function.
                if let Some(response) = as_response(&value) {
                    return serde_json::to_vec(&response).unwrap_or_else(|e| refusal(e.to_string()));
                }
                serde_json::to_vec(&Response { data: Some(value), ..Default::default() })
                    .unwrap_or_else(|e| refusal(e.to_string()))
            }
        }
    }

    fn as_response(value: &Value) -> Option<Response> {
        let object = value.as_object()?;
        if !object.contains_key("template")
            && !object.contains_key("content")
            && !object.contains_key("status")
        {
            return None;
        }
        serde_json::from_value::<ResponseShape>(value.clone()).ok().map(Into::into)
    }

    #[derive(serde::Deserialize)]
    struct ResponseShape {
        #[serde(default)]
        template: Option<String>,
        #[serde(default)]
        model: Option<Value>,
        #[serde(default)]
        status: Option<u16>,
        #[serde(default)]
        error: Option<String>,
    }

    impl From<ResponseShape> for Response {
        fn from(s: ResponseShape) -> Self {
            Response {
                template: s.template,
                model: s.model,
                status: s.status,
                error: s.error,
                ..Default::default()
            }
        }
    }

    fn refusal(message: String) -> Vec<u8> {
        serde_json::to_vec(&Response { error: Some(message), ..Default::default() })
            .unwrap_or_else(|_| br#"{"error":"the refusal could not be encoded"}"#.to_vec())
    }
}

/// Declare a plugin: its id and its exports.
///
/// This generates the three functions the host looks for. There is no main to
/// register from — the host loads the module and calls in — so the exports are
/// named here, where they can be known before anything runs.
///
/// ```ignore
/// plugin! { "myplugin",
///     "home" => home,
///     "refresh" => refresh,
/// }
/// ```
#[macro_export]
macro_rules! plugin {
    ($id:literal $(, $name:literal => $func:path)* $(,)?) => {
        #[no_mangle]
        pub extern "C" fn cog_abi() -> u32 {
            $crate::CONTRACT
        }

        #[no_mangle]
        pub extern "C" fn cog_alloc(size: u32) -> u32 {
            $crate::raw::alloc(size)
        }

        #[no_mangle]
        pub extern "C" fn cog_call(ptr: u32, size: u32) -> u64 {
            let raw = unsafe { $crate::raw::read(ptr, size) };
            let table: &[(&str, &dyn $crate::Export)] = &[
                $(($name, &($func as fn(&$crate::Request, &$crate::Host) -> $crate::Result<::serde_json::Value>))),*
            ];
            $crate::raw::emit(&$crate::raw::dispatch($id, &raw, table))
        }
    };
}

// Base64, by hand and in twenty lines, because pulling a crate in for it would
// be a third dependency in everybody's review for something this size.
mod b64 {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

    pub fn encode(input: &[u8]) -> String {
        let mut out = String::with_capacity(input.len().div_ceil(3) * 4);
        for chunk in input.chunks(3) {
            let b = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
            let n = (b[0] as u32) << 16 | (b[1] as u32) << 8 | b[2] as u32;
            out.push(ALPHABET[(n >> 18 & 63) as usize] as char);
            out.push(ALPHABET[(n >> 12 & 63) as usize] as char);
            out.push(if chunk.len() > 1 { ALPHABET[(n >> 6 & 63) as usize] as char } else { '=' });
            out.push(if chunk.len() > 2 { ALPHABET[(n & 63) as usize] as char } else { '=' });
        }
        out
    }

    pub fn decode(input: &str) -> super::Result<Vec<u8>> {
        let mut out = Vec::with_capacity(input.len() / 4 * 3);
        let mut acc: u32 = 0;
        let mut bits = 0;
        for c in input.bytes() {
            if c == b'=' || c == b'\n' || c == b'\r' {
                continue;
            }
            let v = match ALPHABET.iter().position(|&a| a == c) {
                Some(v) => v as u32,
                None => return Err(super::Error(format!("{c:?} is not base64"))),
            };
            acc = acc << 6 | v;
            bits += 6;
            if bits >= 8 {
                bits -= 8;
                out.push((acc >> bits) as u8);
            }
        }
        Ok(out)
    }
}
