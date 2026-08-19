// What this covers, and why these screens.
//
// Not everything — a first net that tried to be complete would not have
// shipped. These are the paths where a conversion breaks something silently:
// the shell renders at all, the rail's destinations resolve, a plugin's
// contribution appears, and a plugin's own page serves. Each one is a claim
// that has been made in a commit message and could otherwise only be checked
// by somebody remembering to look.

import { test, before, after, describe } from 'node:test'
import assert from 'node:assert/strict'
import { build, start, session, bundle } from './harness.mjs'

describe('the interface', () => {
  let binary, server, ui

  before(async () => {
    binary = build()
    server = await start(binary)
    ui = await session(server)
  })

  after(async () => {
    await ui?.close()
    await server?.stop()
  })

  test('the shell renders and lands on workspaces', async () => {
    assert.match(ui.page.url(), /\/workspaces/)
    await ui.page.waitForSelector('.rail')
  })

  test('every declared client route resolves rather than 404ing', async () => {
    // The server keeps a declared list of the application's screens, and a
    // path outside it is answered as a mistake. These two must agree, and the
    // Go test reads App.tsx to check the same thing from the other side.
    for (const path of ['/workspaces', '/models', '/gears', '/instructions', '/plugins']) {
      const r = await ui.page.request.get(server.url + path)
      assert.equal(r.status(), 200, `${path} should be served`)
    }
  })

  test('a path the application does not have is a 404, not the shell', async () => {
    // The failure this replaced: any typo answered 200 with an HTML document,
    // which made a mistyped URL look like a working page.
    const r = await ui.page.request.get(server.url + '/wrokspaces')
    assert.equal(r.status(), 404)
  })

  test('a missing asset is a 404, not an HTML document', async () => {
    // The worst failure this server can produce: the browser hands an HTML
    // document to its module parser and reports a syntax error at line 1 of a
    // file that looks fine on disk.
    const r = await ui.page.request.get(server.url + '/assets/does-not-exist.js')
    assert.equal(r.status(), 404)
  })

  test('the plugins screen loads and says nothing is installed', async () => {
    await ui.page.goto(server.url + '/plugins')
    await ui.page.waitForSelector('.page')
    assert.match(await ui.page.textContent('.page'), /Nothing installed/i)
  })
})

describe('a plugin', () => {
  let binary, server, ui

  before(async () => {
    binary = build()
    const p = bundle('release-radar', {
      'plugin.yaml': [
        'schema: 1',
        'id: release-radar',
        'name: Release Radar',
        'version: 1.0.0',
        'host:',
        '  contract: 1',
        'nav:',
        '  - area: rail',
        '    label: Releases',
        '    href: /p/release-radar/guide',
        '    order: 500',
        'pages:',
        '  - path: /p/release-radar/guide',
        '    template: release-radar.page.guide',
        '    title: Releases',
        '',
      ].join('\n'),
      'templates/guide.html':
        '{{define "release-radar.page.guide"}}<h1>Releases</h1>{{end}}',
    })
    server = await start(binary, { plugins: [p] })
    ui = await session(server)
  })

  after(async () => {
    await ui?.close()
    await server?.stop()
  })

  test('its nav entry reaches the rail', async () => {
    // nav: was accepted, validated and shown on the plugins page while
    // rendering nothing. This is the test that stops it going inert again.
    const contribution = await ui.page.evaluate(() => window.__COG_PLUGINS__)
    assert.ok(contribution, 'the document should carry the contribution')
    assert.equal(contribution.nav.length, 1)
    assert.equal(contribution.nav[0].label, 'Releases')
    assert.equal(contribution.nav[0].from, 'release-radar')
  })

  test('its page serves, through an ordinary link', async () => {
    // Through a navigation rather than a fetch, because the session cookie is
    // HttpOnly and a bearer token in localStorage does not travel on one — a
    // distinction that cost an hour once and is worth a test.
    await ui.page.goto(server.url + '/p/release-radar/guide')
    assert.match(await ui.page.textContent('body'), /Releases/)
  })

  test('an undeclared path under /p/ is a 404', async () => {
    const r = await ui.page.request.get(server.url + '/p/release-radar/nope')
    assert.equal(r.status(), 404)
  })

  test('the plugins screen shows it as live', async () => {
    await ui.page.goto(server.url + '/plugins')
    await ui.page.waitForSelector('.card')
    const text = await ui.page.textContent('.page')
    assert.match(text, /Release Radar/)
    assert.match(text, /live/)
  })
})

// Approval, driven the way an operator drives it.
//
// This is the gap that made the browser a dead end: the server has had
// approve and revoke since the beginning, the client had neither, and enabling
// is refused until a decision exists. So a plugin uploaded through the screen
// could only ever be finished from a shell — on an install where the whole
// point is that somebody without one can run this.
//
// The test starts a plugin at the exact state an upload leaves it in, and
// walks the two steps that have to exist between there and rendering.
describe('approving a plugin', () => {
  let binary, server, ui

  before(async () => {
    binary = build()
    const p = bundle('needs-a-look', {
      'plugin.yaml': [
        'schema: 1',
        'id: needs-a-look',
        'name: Needs A Look',
        'version: 1.0.0',
        'host:',
        '  contract: 1',
        'pages:',
        '  - path: /p/needs-a-look/page',
        '    template: needs-a-look.page.main',
        '    title: Needs A Look',
        '',
      ].join('\n'),
      'templates/page.html':
        '{{define "needs-a-look.page.main"}}<h1>Needs A Look</h1>{{end}}',
    })
    p.approve = false
    server = await start(binary, { plugins: [p] })
    ui = await session(server)
  })

  after(async () => {
    await ui?.close()
    await server?.stop()
  })

  test('says it is waiting on a person, and offers only that decision', async () => {
    await ui.page.goto(`${server.url}/plugins`)
    await ui.page.waitForSelector('.card')

    assert.equal(await ui.page.locator('.badge').first().innerText(), 'needs approval')

    const buttons = await ui.page.locator('.plugin-actions button').allInnerTexts()
    assert.ok(buttons.includes('Approve'), `no Approve button, got ${buttons.join(', ')}`)
    // Enable before a decision would be refused by the server, and a button
    // that only ever produces an error is worse than no button.
    assert.ok(!buttons.includes('Enable'), 'Enable was offered before approval')
  })

  test('can be approved and then enabled, without leaving the browser', async () => {
    await ui.page.goto(`${server.url}/plugins`)
    await ui.page.waitForSelector('.card')

    await ui.page.getByRole('button', { name: 'Approve' }).click()
    await ui.page.waitForSelector('.plugin-actions button:text-is("Enable")')

    // The decision is on the card afterwards. Who approved it is the question
    // somebody asks months later, and the answer has to survive the click.
    const card = await ui.page.locator('.card').first().innerText()
    assert.ok(/Approved by/.test(card), `no approval trail on the card:\n${card}`)

    await ui.page.getByRole('button', { name: 'Enable' }).click()
    await ui.page.waitForSelector('.plugin-actions button:text-is("Disable")')

    const after = await ui.page.locator('.plugin-actions button').allInnerTexts()
    assert.ok(after.includes('Withdraw approval'), 'no way back out of the decision')
  })
})
