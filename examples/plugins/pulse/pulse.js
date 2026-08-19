// Pulse draws the workspace's own numbers.
//
// SVG built by hand rather than with a charting library, for one reason: a
// plugin that pulled in a chart library would ship it, and the panel is a few
// hundred bytes of markup around numbers that are already in the page. A
// dependency here would outweigh everything it draws.
//
// It reads the host's API with the viewer's own session, so it can only ever
// show what the person looking at it could have seen anyway.

const $ = (id) => document.getElementById(id)

function workspaceFromReferrer() {
  // The panel is an iframe inside a workspace, so the workspace it belongs to
  // is the one whose page opened it. Read from the referrer rather than passed
  // in a query string, because a query string is a thing somebody can edit and
  // this should show the workspace they are actually looking at.
  try {
    const m = new URL(document.referrer).pathname.match(/\/workspaces\/(\d+)/)
    return m ? m[1] : null
  } catch {
    return null
  }
}

function bars(values, labels, colour) {
  const w = 320, h = 90, pad = 2
  const max = Math.max(1, ...values)
  const bw = w / values.length
  const rects = values
    .map((v, i) => {
      const bh = Math.round((v / max) * (h - 8))
      const x = (i * bw + pad).toFixed(1)
      const y = h - bh
      return `<rect x="${x}" y="${y}" width="${(bw - pad * 2).toFixed(1)}" height="${bh}"
        fill="${colour}" opacity="0.85"><title>${labels[i]}: ${v.toLocaleString()}</title></rect>`
    })
    .join('')
  return `<svg viewBox="0 0 ${w} ${h}" role="img" aria-label="bar chart">${rects}</svg>`
}

async function draw() {
  const ws = workspaceFromReferrer()
  if (!ws) {
    $('err').textContent = 'Open this from inside a workspace.'
    $('err').hidden = false
    return
  }
  const r = await fetch(`/api/v1/workspaces/${ws}/metrics`, { credentials: 'same-origin' })
  if (!r.ok) {
    $('err').textContent = `Could not read this workspace's numbers (${r.status}).`
    $('err').hidden = false
    return
  }
  const m = await r.json()

  const totals = m.tokens.reduce((a, d) => a + d.input + d.output, 0)
  $('tokens-total').textContent = totals.toLocaleString()
  $('agents-running').textContent = `${m.agents.running}/${m.agents.total}`
  $('net-requests').textContent = m.network.requests.toLocaleString()

  $('tokens-chart').innerHTML = bars(
    m.tokens.map((d) => d.input + d.output),
    m.tokens.map((d) => d.day),
    'currentColor',
  )
  // Said out loud rather than shown as a confident zero: not every provider
  // reports what a turn cost, and a chart that reads flat because nobody was
  // told is a different fact from one that reads flat because nothing ran.
  $('tokens-note').textContent = m.reported
    ? 'Reported by the model provider.'
    : totals === 0
      ? 'No spend recorded, or this provider does not report usage.'
      : ''

  $('net-chart').innerHTML = bars([m.network.requests, m.network.bytes / 1024], ['requests', 'KiB'], 'currentColor')
}

// A plugin's script is injected into EVERY screen, including ones that have
// nothing of this plugin on them — the sign-in card, somebody else's page. So
// it has to leave when its own markup is not there, rather than reaching for
// an element that does not exist and throwing into a console the operator
// reads as the product being broken.
if (document.querySelector('.pulse')) {
  draw().catch((e) => {
    const err = $('err')
    if (!err) return
    err.textContent = String(e)
    err.hidden = false
  })
}
