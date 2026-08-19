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
