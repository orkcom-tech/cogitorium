// Capture a workspace's blueprint for the documentation.
//
// Scripted rather than taken by hand, because a screenshot in the guide is a
// claim about what the software looks like, and a claim nobody can reproduce
// goes stale without anyone noticing. Run it again after a visual change and
// the picture is current or the diff tells you it moved.
//
//   cd web && node shoot-blueprint.mjs http://127.0.0.1:8802 1 ../docs/assets/13-code-court.png
//
// It lives beside the interface rather than in scripts/ because playwright is a
// devDependency of web/, and Node resolves an ESM import from the script's own
// directory rather than from the working one.
//
// It drives the real interface: picks the canvas-first layout, hides every
// panel but the blueprint, fits the graph, and shoots the canvas element rather
// than the whole page — the sidebar and the chrome are not the subject.
import { chromium } from 'playwright'

const [base, workspace, out] = process.argv.slice(2)
if (!base || !workspace || !out) {
  console.error('usage: shoot-blueprint.mjs <base-url> <workspace-id> <out.png>')
  process.exit(2)
}

const browser = await chromium.launch()
const page = await browser.newPage({
  viewport: { width: 2000, height: 1250 },
  deviceScaleFactor: 2, // a docs image is read at full size; 1x looks soft
  colorScheme: 'dark',
})

await page.goto(`${base}/workspaces/${workspace}`, { waitUntil: 'networkidle' })

const byTitle = (t) => page.locator(`button[title="${t}"]`)

// Canvas-first: the graph takes the room.
await page.getByRole('button', { name: /layout/i }).click()
await page.getByText(/canvas-first/i).first().click()
await page.waitForTimeout(400)

for (const t of ['Hide Chat', 'Hide Agents', 'Hide Files', 'Hide Editor', 'Hide Terminal', 'Hide Inlets']) {
  const b = byTitle(t)
  if (await b.count()) await b.first().click()
}
await page.waitForTimeout(400)

await byTitle('Fit View').first().click()
await page.waitForTimeout(800)

// The edges animate; settle before the shutter or a run of them is caught
// mid-dash and the image looks broken rather than alive.
await page.waitForTimeout(1200)

// Clip to the agents rather than shooting the whole canvas. The outward
// gateway sits where the application puts it, which on a small graph is far
// from everything else, and fitting to include it left the top half of the
// picture empty — a reader looking for the arrangement found a lot of dots.
// The subject is the agents and the wires between them, so that is the frame.
const box = await page.evaluate(() => {
  const nodes = [...document.querySelectorAll('.react-flow__node')]
    .filter((n) => !/echo-page|outward/i.test(n.textContent || ''))
  if (!nodes.length) return null
  const r = nodes.map((n) => n.getBoundingClientRect())
  const pad = 56
  const x = Math.min(...r.map((b) => b.left)) - pad
  const y = Math.min(...r.map((b) => b.top)) - pad
  return {
    x: Math.max(0, x),
    y: Math.max(0, y),
    width: Math.max(...r.map((b) => b.right)) + pad - Math.max(0, x),
    height: Math.max(...r.map((b) => b.bottom)) + pad - Math.max(0, y),
  }
})
await page.screenshot({ path: out, clip: box ?? undefined })

const nodes = await page.locator('.react-flow__node').count()
const edges = await page.locator('.react-flow__edge').count()
console.log(`${out}: ${nodes} nodes, ${edges} edges`)

// Say whether the picture is actually readable, rather than leaving that to
// whoever opens the file. Wire labels are placed by arithmetic in WireEdge, and
// arithmetic that is a little wrong produces an image that still looks like a
// graph — the failure is a label sitting on somebody else's card, which reads
// as belonging to it. Cheap to check here against the rendered boxes, so the
// script reports it instead of shipping a mess quietly.
const collisions = await page.evaluate(() => {
  // Two pixels, because boxes that share an edge are not a mess and reporting
  // them as one trains the reader to ignore the report.
  const slack = 2
  const overlap = (a, b) =>
    a.left < b.right - slack &&
    a.right > b.left + slack &&
    a.top < b.bottom - slack &&
    a.bottom > b.top + slack
  const labels = [...document.querySelectorAll('.bp-wire-label')]
  const boxes = labels.map((l) => l.getBoundingClientRect())
  const out = []
  for (const n of document.querySelectorAll('.react-flow__node')) {
    const r = n.getBoundingClientRect()
    labels.forEach((l, i) => {
      if (overlap(boxes[i], r)) out.push(`"${l.textContent}" over node ${n.dataset.id}`)
    })
  }
  for (let i = 0; i < labels.length; i++)
    for (let j = i + 1; j < labels.length; j++)
      if (overlap(boxes[i], boxes[j]))
        out.push(`"${labels[i].textContent}" over "${labels[j].textContent}"`)
  return out
})
if (collisions.length) {
  console.error(`  ${collisions.length} label collisions:`)
  for (const c of collisions) console.error(`  - ${c}`)
}

await browser.close()
