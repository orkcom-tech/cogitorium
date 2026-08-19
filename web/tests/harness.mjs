// A real server, a real browser, and no mocks.
//
// This is the net every conversion commit needs. The frontend had no automated
// coverage at all, and every step of turning React pages into templates has
// "nothing should visibly change" as its success criterion — which is not a
// criterion anybody can check by hand across sixteen screens, and is exactly
// the kind of claim that is wrong in one place nobody looks.
//
// It uses node:test and the playwright library that is already a devDependency
// rather than @playwright/test, because the runner would be a new dependency
// for a project that keeps eight direct ones and the built-in runner does the
// same job here.

import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { randomBytes } from 'node:crypto'
import { chromium } from 'playwright'

const ROOT = new URL('../..', import.meta.url).pathname
export const PASSWORD = 'correct-horse-battery-staple-42'

/** build compiles the server once per run.
 *
 * Against the real binary rather than a dev server, because what ships is the
 * binary with the interface embedded in it — and a test that passed against
 * `vite dev` would not have exercised the thing an operator installs.
 */
export function build() {
  execFileSync('go', ['build', '-o', join(ROOT, 'bin', 'cogitorium-test'), './cmd/cogitorium'], {
    cwd: ROOT,
    stdio: 'inherit',
  })
  return join(ROOT, 'bin', 'cogitorium-test')
}

/** start runs a server on its own port against its own empty data directory,
 *  so tests cannot see each other's state. */
export async function start(binary, { plugins = [] } = {}) {
  const dataDir = mkdtempSync(join(tmpdir(), 'cogitorium-ui-'))
  const port = 19000 + Math.floor(Math.random() * 900)

  for (const bundle of plugins) {
    const id = bundle.id
    execFileSync(binary, ['plugins', 'install', bundle.path, '--data', dataDir])
    // `approve: false` leaves it where an upload leaves it, which is the state
    // the screen has to be able to get somebody out of. Approving here by
    // default would mean no test ever saw a pending plugin.
    if (bundle.approve !== false) {
      execFileSync(binary, ['plugins', 'approve', id, '--data', dataDir])
      execFileSync(binary, ['plugins', 'enable', id, '--data', dataDir])
    }
  }

  const proc = spawn(binary, ['serve', '--data', dataDir, '--listen', `127.0.0.1:${port}`,
    '--log-level', 'error'], {
    env: { ...process.env, COGITORIUM_SECRET_KEY: randomBytes(32).toString('base64') },
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  const url = `http://127.0.0.1:${port}`
  await waitFor(url)

  return {
    url,
    dataDir,
    async stop() {
      proc.kill('SIGKILL')
      rmSync(dataDir, { recursive: true, force: true })
    },
  }
}

async function waitFor(url) {
  const deadline = Date.now() + 20_000
  for (;;) {
    try {
      const r = await fetch(`${url}/health`)
      if (r.ok) return
    } catch {
      // Not up yet. A connection refused during startup is expected, and
      // treating it as a failure would make this flaky on a slow machine.
    }
    if (Date.now() > deadline) throw new Error(`${url} never became healthy`)
    await new Promise((r) => setTimeout(r, 100))
  }
}

/** session opens a browser and signs in THROUGH THE FORM.
 *
 * Not by pasting a token into localStorage. The session cookie is HttpOnly, so
 * a shortcut that skips the form leaves the browser unable to follow an
 * ordinary link — which is a failure of the shortcut and looks exactly like a
 * failure of the product. Doing it properly also means the setup screen is
 * covered by every test rather than by none.
 */
export async function session(server) {
  const browser = await chromium.launch()
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await page.goto(server.url)

  // By input type rather than by placeholder or label text. The fields carry
  // neither a placeholder nor a name attribute, and matching on visible words
  // would make this break the next time somebody rewords a label — which is a
  // change that should not be able to fail a test about signing in.
  const fields = page.locator('input[type="password"]')
  await fields.first().waitFor({ timeout: 15_000 })
  const count = await fields.count()
  for (let i = 0; i < count; i++) await fields.nth(i).fill(PASSWORD)

  await page.locator('button[type="submit"], form button').first().click()
  await page.waitForSelector('.rail', { timeout: 15_000 })

  return {
    page,
    async close() {
      await browser.close()
    },
  }
}

/** bundle builds a plugin from a directory of files, for tests that need one. */
export function bundle(id, files) {
  const dir = mkdtempSync(join(tmpdir(), 'cogitorium-plugin-'))
  for (const [name, body] of Object.entries(files)) {
    const p = join(dir, name)
    mkdirSync(join(p, '..'), { recursive: true })
    writeFileSync(p, body)
  }
  execFileSync(join(ROOT, 'bin', 'cogitorium-test'), ['plugins', 'build', dir], { cwd: dir })
  // <id>.zip, matching what the catalog fetches from a release. The name used
  // to carry the version and did not match, which is the bug this line now
  // holds in place: if the builder's name drifts again, every browser test
  // that installs a plugin fails here rather than a stranger finding out.
  return { id, path: join(dir, `${id}.zip`) }
}
