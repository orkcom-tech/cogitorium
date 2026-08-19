// A plugin in plain JavaScript. No build step, no node, no .wasm — the engine
// is inside the binary and this file is what ships.

function home(req) {
  // The same nine calls every other tier gets, on the same names.
  const visits = cog.incr('visits')
  const now = cog.now()

  const squares = [1, 2, 3, 4, 5].map((n) => n * n)

  return {
    who: req.ctx.viewer.name || 'nobody',
    visits: visits.value,
    clock: now.rfc3339,
    sum: squares.reduce((a, b) => a + b, 0),
  }
}
