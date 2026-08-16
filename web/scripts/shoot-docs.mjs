// Re-shoot every screenshot the documentation uses, from a running install.
//
// This exists because the last redesign left thirteen screenshots picturing
// software that had been deleted — a left rail, floating windows, a fourteen-
// control appearance dialog, two looks that no longer have names. Nobody
// noticed for one reason: re-shooting was a manual afternoon, so it was never
// done. It is one command now.
//
//   cd web && node scripts/shoot-docs.mjs http://127.0.0.1:8894
//
// The install it points at has to have something in it, and the shots name
// what they expect: workspace 1 is the one with a blueprint worth drawing,
// workspace 4 has a receiver with two tasks, workspace 5 has a schedule and
// three variables. Point this at an empty install and it will happily produce
// nineteen pictures of empty boxes.
//
// It must also run with COGITORIUM_SECRET_KEY set, or the variables shot has
// no secret in it to show being withheld.
//
// Headless on purpose. An earlier attempt to grab these with a screen capture
// caught a private window that happened to be open, which is exactly the kind
// of mistake that only has to happen once.

import { chromium } from 'playwright'
import { mkdir } from 'node:fs/promises'

const base = process.argv[2] || 'http://127.0.0.1:8894'
const out = new URL('../../docs/assets/', import.meta.url).pathname

// One size for every shot, so the set looks like a set and a reader can
// compare two pictures without re-scaling them in their head.
const W = 1440
const H = 900

const shots = []
const shoot = (name, note, fn) => shots.push({ name, note, fn })

/** Wait for the network to settle AND for the app to have painted something. */
async function ready(page) {
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('.sheet', { timeout: 10_000 })
  // The deck slides and the map animates; give one frame past the transition.
  await page.waitForTimeout(700)
}

/** Put the interface in a known state before every shot. */
async function look(page, id, mode) {
  await page.evaluate(
    ([l, m]) => localStorage.setItem('cogitorium.theme', JSON.stringify({ look: l, mode: m })),
    [id, mode],
  )
}

shoot('01-workspaces', 'the workspaces list, coloured, one shared with two teams', async (page) => {
  await page.goto(`${base}/workspaces`)
  await ready(page)
})

shoot('02-workspace-deck', 'the three views and the four overlays, with the roster open', async (page) => {
  await page.goto(`${base}/workspaces/1?layout=reset`)
  await ready(page)
  await page.getByRole('button', { name: 'Agents' }).click()
  await page.waitForTimeout(400)
})

shoot('03-blueprint', 'the blueprint: every wire is a capability', async (page) => {
  await page.goto(`${base}/workspaces/1?layout=reset`)
  await ready(page)
  await page.getByRole('tab', { name: 'Blueprint' }).click()
  await page.waitForTimeout(900)
})

shoot('04-appearance', 'appearance: a look and a mode, and nothing else', async (page) => {
  await page.goto(`${base}/workspaces`)
  await ready(page)
  await page.locator('.theme-chip').click()
  await page.waitForTimeout(400)
})

shoot('05-editor', 'the Editor view: the tree, the file and a shell you start yourself', async (page) => {
  await page.goto(`${base}/workspaces/1?layout=reset`)
  await ready(page)
  await page.getByRole('tab', { name: 'Editor' }).click()
  await page.waitForTimeout(900)
})

shoot('06-people', 'People, and the access map of who can reach what', async (page) => {
  await page.goto(`${base}/people`)
  await ready(page)
  await page.waitForTimeout(900)
})

shoot('07-gears', 'the gear catalogue: pending gears carry the way in', async (page) => {
  await page.goto(`${base}/gears`)
  await ready(page)
})

shoot('08-gear-review', 'what approving a gear grants, stated before you approve it', async (page) => {
  await page.goto(`${base}/gears`)
  await ready(page)
  await page.getByRole('button', { name: 'review & approve' }).first().click()
  await page.waitForTimeout(500)
})

shoot('09-map', 'the install map: people at the centre, workspaces on the outside', async (page) => {
  await page.goto(`${base}/map`)
  await ready(page)
  await page.waitForTimeout(1600)
})

shoot('10-map-open', 'one workspace opened on the map, its agents and memory drawn', async (page) => {
  await page.goto(`${base}/map`)
  await ready(page)
  await page.waitForTimeout(1400)
  const box = await page.locator('.imap-canvas').boundingBox()
  // The workspace ring sits at a known bearing from the centre; the first one
  // is due north of it.
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2 - 0.62 * 620 * 0.86)
  await page.waitForTimeout(2000)
})

shoot('11-instructions', 'the instruction library: text an agent pins, versioned in Contextverse', async (page) => {
  await page.goto(`${base}/instructions`)
  await ready(page)
})

shoot('12-models', 'the model catalogue: providers, and what each one offers', async (page) => {
  await page.goto(`${base}/models`)
  await ready(page)
})

shoot('13-account', 'the account menu: the install-wide pages, and the way out', async (page) => {
  await page.goto(`${base}/workspaces`)
  await ready(page)
  await page.locator('.acct-btn').click()
  await page.waitForTimeout(300)
})

// The three remaining overlays and the two admin pages. The guide has a
// section per screen, so a screen with no picture is a section with none.
//
// Each is shot on the workspace that actually has the thing in it. The first
// cut pointed all three at workspace 1, which has no receivers, no schedule
// and no variables — three pictures of an empty box, illustrating nothing.
// ?layout=reset because the deck remembers which view you were on, and these
// run in one browser after the Editor shot: without it the roster, the
// receivers and the variables were all photographed over a file tree.
const overlay = (name, ws, button, note, scroll = 0) =>
  shoot(name, note, async (page) => {
    await page.goto(`${base}/workspaces/${ws}?layout=reset`)
    await ready(page)
    await page.getByRole('button', { name: button }).click()
    await page.waitForTimeout(400)
    // Some overlays put an explanation and a form above the list they are
    // about. Scroll to the part worth photographing.
    if (scroll) {
      await page.locator('.dk-overlay-body').evaluate((el, y) => el.scrollBy(0, y), scroll)
      await page.waitForTimeout(300)
    }
  })

overlay('15-receivers', 4, 'Receivers', 'a receiver with its two tasks, on support triage', 320)
overlay('16-queue', 5, 'Queue', 'the queue, and the schedule that fills it at 03:00')
overlay('17-variables', 5, 'Variables', "the workspace's own variables, and a secret that is not shown", 460)

shoot('18-context', 'the context browser: every version Contextverse kept', async (page) => {
  await page.goto(`${base}/context`)
  await ready(page)
  await page.waitForTimeout(500)
})

shoot('19-terminal', 'the terminal: a shell on the host, admin only, started by hand', async (page) => {
  await page.goto(`${base}/terminal`)
  await ready(page)
  await page.waitForTimeout(500)
})

shoot('14-dark', 'every look is drawn in both — the same install, after dark', async (page) => {
  await page.goto(`${base}/map`)
  await look(page, 'air', 'dark')
  await page.reload()
  await ready(page)
  await page.waitForTimeout(1600)
})

const only = process.argv[3]

const browser = await chromium.launch()
const ctx = await browser.newContext({
  viewport: { width: W, height: H },
  deviceScaleFactor: 2, // retina, so the images survive being scaled in a README
  colorScheme: 'light',
})
const page = await ctx.newPage()

await mkdir(out, { recursive: true })

// Every shot starts from the same appearance, or the set is a patchwork.
await page.goto(base)
await look(page, 'air', 'light')

let done = 0
for (const s of shots) {
  if (only && !s.name.includes(only)) continue
  try {
    if (!s.name.startsWith('14')) await look(page, 'air', 'light')
    await s.fn(page)
    await page.screenshot({ path: `${out}${s.name}.png` })
    console.log(`  ${s.name}.png — ${s.note}`)
    done++
  } catch (err) {
    // A failed shot is reported and skipped rather than aborting the run: one
    // missing picture is a fixable gap, and losing the other thirteen to it is
    // not a trade worth making.
    console.error(`  FAILED ${s.name}: ${err.message.split('\n')[0]}`)
  }
}

await browser.close()
console.log(`\n${done}/${shots.length} shot into docs/assets/`)
