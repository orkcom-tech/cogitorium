# Vendored, not fetched

These are here rather than on a CDN because the interface fetches nothing from
the network — no fonts, no scripts, no analytics — and an exception for a
hypermedia library would be an exception that reads the same in a packet
capture as any other.

| file | version | sha256 of what was vendored |
|---|---|---|
| `htmx.min.js` | 2.0.4 | `e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447` |
| `htmx-sse.js` | 2.2.2 | `83eca6fa0611fe2b0bf1700b424b88b5eced38ef448ef9760a2ea08fbc875611` |

## Why htmx and not also Datastar

The plan named both. Loading two hypermedia dialects unconditionally on every
page is two vocabularies every plugin author has to learn before they can
override anything, and the second earns nothing the first does not already do.
One dialect is a smaller promise to keep and a smaller thing to explain.

The SSE extension is here because the chat stage streams, and a conversion that
could not carry streaming would stop at the screen that matters most.
