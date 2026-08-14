// The documentation's screenshots, taken from a running install.
//
// They used to be shot by hand, which is why they drifted: a look changes and
// twelve PNGs quietly go on showing the old one. This takes them all from one
// server in one pass, so re-shooting after a visual change is a command rather
// than an afternoon.
//
// Usage:
//   make build
//   ./bin/cogitorium serve --data <a demo data dir> --listen 127.0.0.1:8899 &
//   node scripts/shots.mjs http://127.0.0.1:8899
//
// The install has to be on loopback: that is what makes the browser the admin
// without a token, and it is the same rule the product documents.

import { mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

// Playwright is the UI's devDependency and lives in web/node_modules; an
// import from this directory would look in scripts/ and find nothing. Resolved
// from there explicitly rather than moving this file under web/, because it
// shoots the whole product and not the front end.
const require = createRequire(join(dirname(fileURLToPath(import.meta.url)), '..', 'web', 'package.json'))
const { chromium } = require('playwright')

const BASE = process.argv[2] || 'http://127.0.0.1:8688'
const OUT = join(dirname(fileURLToPath(import.meta.url)), '..', 'docs', 'assets')

// 2x, because these are read on retina displays and a 1x PNG of an interface
// this dense is a smear. The page is laid out at the CSS size below.
const VIEW = { width: 1440, height: 900 }
const SCALE = 2

/** Wait for the app to have painted something real, not just mounted. */
async function ready(page) {
  await page.waitForSelector('.layout, .page, .login', { timeout: 15000 })
  await page.waitForTimeout(450)
}

/**
 * Pick a look the way an operator does: open Appearance and click it.
 *
 * The first version wrote {look, mode} straight into localStorage, which was
 * wrong in a way worth remembering — a stored theme is read field by field, so
 * a two-field object takes the PALETTE from the fallback table and shot a
 * near-black ground with paper panels on it. Clicking the real control runs
 * withLook, which is the only thing that carries a whole signature.
 *
 * A fresh context needs nothing for the default look.
 */
async function chooseLook(page, look) {
  if (look === 'sketch') return // the default; a clean profile is already there
  await page.click('button[title="Palette"]')
  await page.waitForSelector('.look-choice')
  await page.click(`.look-option:has(.look-${look})`)
  await page.waitForFunction(
    (l) => document.documentElement.getAttribute('data-look') === l,
    look,
  )
  await page.keyboard.press('Escape').catch(() => {})
  const close = page.locator('.theme-menu button[title="Close"]')
  if (await close.count()) await close.first().click()
  await page.waitForTimeout(200)
}

/**
 * Put the graph on screen.
 *
 * A workspace opens on the conversation, so a shot named "wired" was a picture
 * of an empty chat box — the thing the caption promised was not in the frame.
 * The Blueprint panel is turned on, the Chat turned off to give it the room,
 * and the view fitted so the whole arrangement is visible rather than a corner
 * of it.
 */
/** Apply one of the ready-made arrangements. */
async function usePreset(page, preset) {
  await page.click('button[title="Layouts"]')
  await page.waitForSelector('.layout-menu')
  await page.click(`.layout-menu button:has-text("${preset}")`)
  await page.waitForTimeout(700)
}

async function showGraph(page, preset = 'Canvas-first') {
  // The product ships arrangements for exactly this; toggling panels by hand
  // instead left a dead column where the chat had been, because hiding a dock
  // does not hand its width to anything.
  await page.click('button[title="Layouts"]')
  await page.waitForSelector('.layout-menu')
  await page.click(`.layout-menu button:has-text("${preset}")`)
  await page.waitForSelector('.react-flow__node')
  await page.waitForTimeout(800)

  // Then FIT, and check that it worked, rather than clicking and hoping.
  //
  // Both failures here were silent. React Flow will not zoom below 0.5, so in a
  // narrow dock the fit runs, clamps, and leaves a node off the edge; and a fit
  // clicked twice in quick succession left the graph parked somewhere with no
  // nodes in frame at all. A screenshot of either builds, uploads and ships —
  // the only thing that catches it is measuring.
  const fit = page.locator('.react-flow__controls-fitview')
  for (let attempt = 0; attempt < 3; attempt++) {
    if (await fit.count()) await fit.first().click()
    await page.waitForTimeout(700)
    const framed = await page.evaluate(() => {
      const pane = document.querySelector('.react-flow')
      const nodes = [...document.querySelectorAll('.react-flow__node')]
      if (!pane || !nodes.length) return { ok: false, why: 'no nodes' }
      const p = pane.getBoundingClientRect()
      const out = nodes.filter((n) => {
        const r = n.getBoundingClientRect()
        return r.left < p.left - 1 || r.right > p.right + 1 || r.top < p.top - 1 || r.bottom > p.bottom + 1
      })
      return { ok: out.length === 0, why: `${out.length}/${nodes.length} outside` }
    })
    if (framed.ok) return
    if (attempt === 2) throw new Error(`the graph is not in frame: ${framed.why}`)
  }
}

// A wider page for the shots whose subject is the graph.
//
// Widening alone does not rescue "Wire up": that arrangement gives the
// blueprint a fixed 520px dock, and with React Flow refusing to zoom below 0.5
// a nine-node graph cannot fit in it at any page width. So the graph shots use
// the arrangement that hands it the main dock — which is what an operator does
// too, the moment they want to see the whole thing.
const WIDE = { width: 1760, height: 1000 }


/** Open an agent in the inspector — the roster's first card. */
async function openAgent(page) {
  await page.click('.agent-card >> nth=0')
  await page.waitForTimeout(500)
}

/** Float a panel out, which is what the "floating windows" shot is about. */
async function floatPanel(page) {
  const place = page.locator('button[title="Place this panel"]').first()
  await place.click()
  await page.waitForTimeout(250)
  const float = page.locator('.bn-menu button:has-text("Float")').first()
  if (await float.count()) await float.click()
  await page.waitForTimeout(600)
}

/** A warmer palette, to show the dials are independent of the look. */
async function warmPalette(page) {
  await page.click('button[title="Palette"]')
  await page.waitForSelector('.theme-presets')
  await page.click('.theme-preset[title="Ember"]')
  await page.waitForTimeout(400)
  const close = page.locator('.theme-menu button[title="Close"]').first()
  if (await close.count()) await close.click()
  await page.waitForTimeout(300)
}

const shots = [
  { file: '01-workspaces.png', path: '/workspaces', look: 'sketch' },
  { file: '02-workspace-wired.png', path: '/workspaces/1', look: 'sketch', view: WIDE, prep: showGraph },
  { file: '08-canvas-first.png', path: '/workspaces/1', look: 'canvas', view: WIDE, prep: showGraph },
  { file: '07-gears.png', path: '/gears', look: 'sketch' },
  { file: '03-build-layout.png', path: '/workspaces/1', look: 'sketch', view: WIDE, prep: (p) => usePreset(p, 'Build') },
  { file: '05-agent.png', path: '/workspaces/1', look: 'sketch', prep: openAgent },
  { file: '06-people-map.png', path: '/people', look: 'sketch', view: WIDE },
  { file: '09-palette.png', path: '/workspaces/1', look: 'sketch', prep: warmPalette },
  { file: '11-floats.png', path: '/workspaces/1', look: 'sketch', view: WIDE, prep: floatPanel },
  { file: '13-instrument.png', path: '/workspaces/1', look: 'instrument', view: WIDE, prep: showGraph },
]

const ctxOpts = { viewport: VIEW, deviceScaleFactor: SCALE, colorScheme: 'light' }

const browser = await chromium.launch()
await mkdir(OUT, { recursive: true })

for (const shot of shots) {
  const ctx = await browser.newContext({
    ...ctxOpts,
    viewport: shot.view || VIEW,
    colorScheme: shot.look === 'sketch' ? 'light' : 'dark',
  })
  const page = await ctx.newPage()
  // The look is chosen on a page that has the control, then carried to the
  // target by localStorage — which is where the app keeps it anyway.
  await page.goto(BASE + '/workspaces', { waitUntil: 'networkidle' })
  await ready(page)
  await chooseLook(page, shot.look)
  if (shot.path !== '/workspaces') {
    await page.goto(BASE + shot.path, { waitUntil: 'networkidle' })
    await ready(page)
  }
  if (shot.prep) await shot.prep(page)
  await page.screenshot({ path: join(OUT, shot.file) })
  console.log('  ', shot.file, '·', shot.look)
  await ctx.close()
}

// The Appearance menu, which has to be opened rather than navigated to — and
// is the one shot whose whole subject is the three looks side by side.
{
  const ctx = await browser.newContext(ctxOpts)
  const page = await ctx.newPage()
  await page.goto(BASE + '/workspaces', { waitUntil: 'networkidle' })
  await ready(page)
  await page.click('button[title="Palette"]')
  await page.waitForSelector('.look-choice')
  await page.waitForTimeout(250)
  await page.screenshot({ path: join(OUT, '04-appearance.png') })
  console.log('   04-appearance.png · sketch')
  await ctx.close()
}

await browser.close()
console.log('done →', OUT)
