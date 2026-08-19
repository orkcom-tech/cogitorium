// Re-shoot every screenshot the documentation uses, from a running install.
//
// This exists because the last redesign left thirteen screenshots picturing
// software that had been deleted — a left rail, floating windows, a fourteen-
// control appearance dialog, two looks that no longer have names. Nobody
// noticed for one reason: re-shooting was a manual afternoon, so it was never
// done. It is one command now, and a test fails when a picture is older than
// the interface it claims to show.
//
//   cd web && node scripts/shoot-docs.mjs http://127.0.0.1:8894
//
// The install it points at has to have something in it, and the shots name
// what they expect: workspace 1 is the one with a blueprint worth drawing,
// workspace 4 has a receiver with two tasks, workspace 5 has a schedule and
// three variables. Point this at an empty install and it will happily produce
// twenty pictures of empty boxes.
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

// The shooter needs a token like any other client.
//
// It did not, once: a request from this machine was the admin, so a browser
// pointed at a local install was already signed in and this script had nothing
// to prove. Now every screen behind the sign-in card is behind a token, and
// without one every shot would picture the same login form.
const token = process.env.COGITORIUM_TOKEN
if (!token) {
  console.error(
    'Set COGITORIUM_TOKEN to a token for the install being shot. One way:\n' +
      '  export COGITORIUM_TOKEN=$(cogitorium login --server ' + base + ' --user admin)',
  )
  process.exit(2)
}

// One size for every shot, so the set looks like a set and a reader can
// compare two pictures without re-scaling them in their head.
const W = 1440
const H = 900

const shots = []
const shoot = (name, note, fn) => shots.push({ name, note, fn })

/** Wait for the network to settle AND for the app to have painted something. */
async function ready(page) {
  await page.waitForLoadState('networkidle')
  // The cavity is the hole in the frame — it exists as soon as the shell has
  // painted, on every screen, which is what makes it the thing to wait for.
  await page.waitForSelector('.cavity', { timeout: 10_000 })
  // Stages slide, drawers crawl out and the map animates; one frame past the
  // longest transition in the product.
  await page.waitForTimeout(800)
}

/**
 * Park the pointer somewhere harmless before the shutter.
 *
 * Playwright leaves the mouse wherever it last clicked, and this interface
 * answers a hover: a rail button raises its name in a tooltip, a row takes a
 * tint. Half the old set had a tooltip hanging over the thing it was meant to
 * show, which reads as a rendering bug rather than as a hover state.
 */
async function settle(page) {
  await page.mouse.move(W - 8, H - 8)
  await page.waitForTimeout(250)
}

/** Put the interface in a known appearance. Two choices now, not eleven.
 *
 * A cool turquoise-blue rather than the teal that was here: the documentation
 * is read mostly in light, and a green-leaning accent on a light ground reads
 * warm and slightly sickly at small sizes. This one sits on the blue side of
 * turquoise and stays cold, which is what the product should look like the
 * first time somebody sees it.
 */
const ACCENT = '#0e7490'

async function theme(page, mode) {
  await page.evaluate(
    ([m, accent]) => localStorage.setItem('cogitorium.theme', JSON.stringify({ mode: m, accent })),
    [mode, ACCENT],
  )
}

/** A rail button, by the name its tooltip and its screen-reader label carry. */
const rail = (page, name) => page.getByRole('button', { name, exact: true })

/**
 * A drawer, out over the blueprint.
 *
 * Over the BLUEPRINT and not the chat, because that is the picture that shows
 * what a drawer is: the frame growing inward while the work stays where it
 * was, with the canvas visible beside it. A drawer photographed over an empty
 * transcript shows a panel next to nothing and could be a page.
 *
 * These used to be shot at /gears and /context — the standalone routes left
 * over from the navigation the rail replaced — so two pictures in the set
 * showed a window where every other one shows a drawer.
 */
async function overBlueprint(page, drawer) {
  await page.goto(`${base}/workspaces/1`)
  await ready(page)
  await rail(page, 'Blueprint').click()
  await page.waitForTimeout(1400)
  await rail(page, drawer).click()
  await page.waitForTimeout(800)
}

shoot('01-workspaces', 'the workspaces list, coloured, one shared with two teams', async (page) => {
  await page.goto(`${base}/workspaces`)
  await ready(page)
})

shoot('02-workspace-chat', 'the frame: rail on the bezel, chat in the cavity, agents crawled out', async (page) => {
  await page.goto(`${base}/workspaces/1`)
  await ready(page)
  await rail(page, 'Agents').click()
  await page.waitForTimeout(700)
})

shoot('03-blueprint', 'the blueprint: every wire is a capability, the clock says when it next fires, and the controls float on it', async (page) => {
  await page.goto(`${base}/workspaces/1`)
  await ready(page)
  await rail(page, 'Blueprint').click()
  await page.waitForTimeout(1400)
})

shoot('04-appearance', 'appearance: light or dark, and a colour that is yours', async (page) => {
  // The map rather than the workspaces list: the account, the appearance and
  // the update notice live on the rail the CLIENT draws, and /workspaces is a
  // screen the server renders now.
  await page.goto(`${base}/map`)
  await ready(page)
  await rail(page, 'Appearance').click()
  await page.waitForTimeout(500)
})

shoot('05-editor', 'the Editor stage: the tree flush to the frame, the file filling the rest', async (page) => {
  await page.goto(`${base}/workspaces/1`)
  await ready(page)
  await rail(page, 'Editor').click()
  await page.waitForTimeout(1200)
})

shoot('06-people', 'People, and the access map of who can reach what', async (page) => {
  await page.goto(`${base}/people`)
  await ready(page)
  await page.waitForTimeout(900)
})

// The gear catalogue as it is actually met: a drawer that crawls out over the
// work, not a page of its own. It has a standalone route as well, left over
// from the navigation the rail replaced, and shooting THAT was why these two
// pictures showed a window where every other one shows a drawer.
shoot('07-gears', 'the gear catalogue, where it is met: a drawer out over the blueprint', async (page) => {
  await overBlueprint(page, 'Gears')
})

shoot('08-gear-review', 'what approving a gear grants, stated before you approve it', async (page) => {
  await overBlueprint(page, 'Gears')
  await page.getByRole('link', { name: /review . approve/ }).first().click()
  await page.waitForTimeout(700)
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

shoot('13-rail-menu', 'the rail: the install-wide pages, and the way out', async (page) => {
  await page.goto(`${base}/map`)
  await ready(page)
  await rail(page, 'More').click()
  await page.waitForTimeout(400)
})

// The drawers. Each is shot on the workspace that actually has the thing in
// it: the first cut pointed all of them at workspace 1, which has no
// receivers, no schedule and no variables — three pictures of an empty box,
// illustrating nothing.
const drawer = (name, ws, button, note, scroll = 0) =>
  shoot(name, note, async (page) => {
    await page.goto(`${base}/workspaces/${ws}`)
    await ready(page)
    await rail(page, button).click()
    await page.waitForTimeout(700)
    // Some drawers put an explanation and a form above the list they are
    // about. Scroll to the part worth photographing.
    if (scroll) {
      await page.locator('.drawer-body').evaluate((el, y) => el.scrollBy(0, y), scroll)
      await page.waitForTimeout(300)
    }
  })

drawer('15-receivers', 4, 'Receivers', 'a receiver with its two tasks, on support triage', 320)
drawer('16-queue', 5, 'Queue', 'the queue, and the schedule that fills it at 03:00')
drawer('17-variables', 5, 'Variables', "the workspace's own variables, and a secret that is not shown", 460)

shoot('18-context', 'the context space as a drawer: search inside the files, every version kept', async (page) => {
  await overBlueprint(page, 'Context')
})

shoot('19-terminal', 'the terminal: a shell on the host, admin only, started by hand', async (page) => {
  await page.goto(`${base}/terminal`)
  await ready(page)
  await page.waitForTimeout(500)
})

shoot('20-gear-approvals', 'who let this code run, when, to which version, and with what', async (page) => {
  // Straight to the open gear. The trail is a disclosure inside the card, and
  // reaching it by clicking through the drawer navigated away from the drawer
  // — the link goes to the page, because a screen the server renders reaches
  // its actions by going somewhere.
  await page.goto(`${base}/gears?open=1`)
  await ready(page)
  await page.getByText('who approved it').first().click()
  await page.waitForTimeout(600)
  await page.locator('main.page').first().evaluate((el) => el.scrollBy(0, 500))
  await page.waitForTimeout(300)
})

shoot('22-plugins', 'the plugins screen: what is installed, and the library beside it', async (page) => {
  await page.goto(`${base}/plugins`)
  await ready(page)
})

shoot('23-versions', "a workflow's history: what it was, and the way back", async (page) => {
  await overBlueprint(page, 'Versions')
})

shoot('21-context-search', 'finding a memory without already knowing its path', async (page) => {
  await overBlueprint(page, 'Context')
  await page.getByPlaceholder('search inside the files…').fill('context')
  await page.getByRole('button', { name: 'search', exact: true }).click()
  await page.waitForTimeout(1000)
})

shoot('14-dark', 'the same install after dark — both modes, one geometry', async (page) => {
  await page.goto(`${base}/map`)
  await theme(page, 'dark')
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

// Signed in before the app is ever rendered, so it does not flash the sign-in
// card on its way to the first screen — and so a reload mid-run cannot land on
// one.
//
// The cookie rather than localStorage, because that is what a signed-in browser
// actually holds: the app reads no token from JavaScript any more, so seeding
// one there would leave every shot on the sign-in card.
await ctx.addCookies([
  {
    name: 'cogitorium_session',
    value: token,
    url: base,
    httpOnly: true,
    sameSite: 'Lax',
  },
])

// Every shot starts from the same appearance, or the set is a patchwork.
await page.goto(base)
await theme(page, 'light')

let done = 0
for (const s of shots) {
  if (only && !s.name.includes(only)) continue
  try {
    if (!s.name.startsWith('14')) await theme(page, 'light')
    await s.fn(page)
    await settle(page)
    await page.screenshot({ path: `${out}${s.name}.png` })
    console.log(`  ${s.name}.png — ${s.note}`)
    done++
  } catch (err) {
    // A failed shot is reported and skipped rather than aborting the run: one
    // missing picture is a fixable gap, and losing the other nineteen to it is
    // not a trade worth making.
    console.error(`  FAILED ${s.name}: ${err.message.split('\n')[0]}`)
  }
}

await browser.close()

console.log(`\n${done}/${shots.length} shot into docs/assets/`)
