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

  // The application's own bundle, by name, from the document that asks for it.
  //
  // A handler registered on /assets/ once shadowed this — the host's vendored
  // hypermedia layer was put there, and Vite writes the interface's build
  // output to the same prefix. Every screen answered 404 for its own
  // JavaScript, which presents as a blank page rather than as a routing
  // mistake, and every test in this file failed at once with a timeout.
  test('the interface can load the bundle its own document asks for', async () => {
    const res = await ui.page.goto(`${server.url}/workspaces`)
    assert.equal(res.status(), 200)

    const html = await res.text()
    for (const href of html.match(/\/assets\/[A-Za-z0-9._-]+/g) ?? []) {
      const asset = await ui.page.request.get(`${server.url}${href}`)
      assert.equal(asset.status(), 200, `${href} is ${asset.status()}`)
    }

    // And the host's own vendored layer, which must not live on that prefix.
    const htmx = await ui.page.request.get(`${server.url}/cog/htmx.min.js`)
    assert.equal(htmx.status(), 200)
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

  // nav: was accepted, validated and shown on the plugins page while rendering
  // nothing. This is the test that stops it going inert again — and it has to
  // check both rails, because there are two: a screen the server renders draws
  // its own, and the four screens the application still owns draw theirs from
  // a payload spliced into the document.
  test('its nav entry reaches the rail the server draws', async () => {
    await ui.page.goto(server.url + '/plugins')
    await ui.page.waitForSelector('nav.rail')
    const entry = ui.page.locator('nav.rail a[href="/p/release-radar/guide"]')
    assert.equal(await entry.count(), 1, "the plugin's entry is not in the rendered rail")
    assert.equal(await entry.getAttribute('title'), 'Releases')
  })

  test('its nav entry reaches the rail the application draws', async () => {
    // /map is one of the four the client still owns: a drawn canvas, which a
    // template cannot be.
    await ui.page.goto(server.url + '/map')
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

// The first screen of the product served as a template rather than by the
// application — and the thing that makes the whole plugin system worth having:
// a plugin can take over part of a screen the core never designated as
// extensible.
//
// Until this landed, "override a screen" was a promise with nothing behind it.
// The template surface rendered plugin pages only, so there was nothing to
// override except the frame around a plugin's own markup.
describe('a converted screen', () => {
  let binary, server, ui

  before(async () => {
    binary = build()
    const p = bundle('rowskin', {
      'plugin.yaml': [
        'schema: 1',
        'id: rowskin',
        'name: Row Skin',
        'version: 1.0.0',
        'host:',
        '  contract: 1',
        'overrides:',
        '  - cog.row.instruction',
        '',
      ].join('\n'),
      'templates/row.html':
        '{{define "cog.row.instruction"}}<article class="card skinned">' +
        '<h3>{{.Name}}</h3></article>{{end}}',
    })
    server = await start(binary, { plugins: [p] })
    ui = await session(server)
  })

  after(async () => {
    await ui?.close()
    await server?.stop()
  })

  // Nothing is seeded here on purpose. Writing an instruction stores its text
  // in Contextverse, which this harness does not run — an earlier version of
  // this test seeded through the API and hung forever waiting on a process
  // that was never going to answer. What the browser uniquely proves is that
  // the page is served as a document rather than by the application; that a
  // plugin's body reaches the rows is a composition question, and
  // internal/view answers it without a browser.
  test('it is rendered by the server, inside the product', async () => {
    await ui.page.goto(`${server.url}/instructions`)
    await ui.page.waitForSelector('.library-list')

    // The rail is there, so a page served this way sits inside the product
    // rather than being a bare document dropped beside it.
    assert.ok(await ui.page.locator('nav.rail').count(), 'the page has no rail')
    // And React was not booted over the top of it.
    assert.equal(await ui.page.locator('#root').count(), 0, 'the application mounted over the template')
  })

  // A screen with correct markup and no stylesheet is a broken product, and it
  // is the failure a server-rendered page invites: the document is built from
  // the template stack rather than from index.html, so nothing carries the
  // application's CSS into it unless something does it deliberately. For a
  // while nothing did, and every converted screen went out unstyled.
  test('it is styled, and not merely correct', async () => {
    await ui.page.goto(`${server.url}/instructions`)
    await ui.page.waitForSelector('.library-list')

    const sheets = await ui.page.evaluate(() => document.styleSheets.length)
    assert.ok(sheets > 0, 'the document carries no stylesheet at all')

    // The rail is the visible half of it: its shapes come from the same file,
    // and an unstyled rail is a column of blue underlined words.
    const rail = await ui.page.evaluate(() => {
      const el = document.querySelector('.rail')
      return el ? getComputedStyle(el).position : null
    })
    assert.equal(rail, 'fixed', 'the rail is unstyled, so the page has no frame')
  })

  test('the empty state says which kind of empty it is', async () => {
    await ui.page.goto(`${server.url}/instructions`)
    await ui.page.waitForSelector('.library-list')
    assert.match(await ui.page.locator('.library-list').innerText(), /library is empty/)

    // Narrowed to nothing is a different sentence from having nothing, and
    // showing the wrong one tells somebody their library is gone.
    await ui.page.goto(`${server.url}/instructions?q=nothingmatchesthis`)
    await ui.page.waitForSelector('.library-list')
    assert.match(await ui.page.locator('.library-list').innerText(), /Nothing in the library matches/)
  })

  test('the plugin overriding its row is live', async () => {
    await ui.page.goto(`${server.url}/plugins`)
    await ui.page.waitForSelector('.card')
    const card = await ui.page.locator('.card').first().innerText()
    assert.match(card, /cog\.row\.instruction/)
    assert.equal(await ui.page.locator('.badge.is-ok').first().innerText(), 'live')
  })
})

// The rule that stopped a converted screen from publishing itself.
//
// Everything outside /api/ was anonymous, which was correct while everything
// outside /api/ was the application's shell and its static files — those hold
// nothing, because the application fetches its own data with a token. The
// first converted screen broke that silently: /instructions answered 200 with
// the library in it to anybody who asked.
describe('a screen the server renders', () => {
  let binary, server

  before(async () => {
    binary = build()
    server = await start(binary)
  })

  after(async () => {
    await server?.stop()
  })

  test('needs a credential, and sends a person to sign in rather than a status', async () => {
    for (const path of ['/instructions', '/models', '/models/providers']) {
      const res = await fetch(`${server.url}${path}`, { redirect: 'manual' })
      assert.equal(res.status, 303, `${path} answered ${res.status} with no credential`)
      assert.equal(res.headers.get('location'), '/')
    }
  })

  test('the application shell stays reachable, or nobody could sign in', async () => {
    const res = await fetch(`${server.url}/`)
    assert.equal(res.status, 200)
  })

  test('the API keeps its status, because a script needs one', async () => {
    const res = await fetch(`${server.url}/api/v1/workspaces`)
    assert.equal(res.status, 401)
  })
})

// Every drawer in the workspace, opened the way a person opens it.
//
// This is the test that would have caught the worst fault in the product: the
// server rendered all of them correctly, answered 200, and the browser never
// asked. React mounts the container and htmx scans the document only once, at
// load — so every drawer opened empty while every server-side test passed.
//
// It is a browser test on purpose. Nothing short of a real page can tell the
// difference between "the server can render this" and "the person sees it".
describe('the drawers a person opens', () => {
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

  test('every one of them arrives with something in it', async () => {
    // A workspace needs an orchestrator, an orchestrator needs a model, and a
    // model needs a provider — so all three, through the page's own session.
    // The address is never reached: nothing here asks the provider anything.
    const created = await ui.page.evaluate(async (url) => {
      const post = async (path, body) => {
        const res = await fetch(url + path, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          credentials: 'include',
          body: JSON.stringify(body),
        })
        if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`)
        return res.json()
      }
      const provider = await post('/api/v1/providers', {
        name: 'nowhere',
        type: 'openai-compatible',
        base_url: 'http://127.0.0.1:9',
        api_key: 'unused',
      })
      const model = await post('/api/v1/models', {
        provider_id: provider.id,
        model_name: 'nothing',
        label: 'Nothing',
      })
      const ws = await post('/api/v1/workspaces', {
        name: 'drawers',
        description: 'for the drawers test',
        orchestrator_model_id: model.id,
      })
      return ws.id
    }, server.url)
    assert.ok(created, 'the workspace could not be created')

    await ui.page.goto(`${server.url}/workspaces/${created}`)
    await ui.page.waitForSelector('nav.rail')

    // Not every drawer: the ones whose body the server renders. A drawer the
    // client still draws is not what this is about.
    for (const name of ['Agents', 'Gears', 'MCP servers', 'Receivers', 'Queue', 'Variables']) {
      await ui.page.locator(`nav.rail button:has-text("${name}")`).first().click()
      const body = ui.page.locator('.drawer-body')
      await body.waitFor({ state: 'visible', timeout: 10_000 })
      await ui.page.waitForFunction(
        () => (document.querySelector('.drawer-body')?.innerText ?? '').trim().length > 0,
        { timeout: 10_000 },
      ).catch(() => {})
      const text = (await body.innerText()).trim()
      assert.ok(text.length > 0, `the ${name} drawer opened empty`)
    }
  })
})

// A panel the server renders, inside a page the client still owns.
//
// This is the seam the conversion is happening on. The workspace — its chat,
// its blueprint, its editor — is still the application's, and converting all
// of it before anything inside could be overridden would mean nothing was
// overridable for a long time. A drawer is self-contained, which makes it the
// right size to move first.
describe('a drawer the server renders', () => {
  let binary, server, ui

  before(async () => {
    binary = build()
    const p = bundle('drskin', {
      'plugin.yaml': [
        'schema: 1', 'id: drskin', 'name: Drawer Skin', 'version: 1.0.0',
        'host:', '  contract: 1', 'overrides:', '  - cog.row.gear', '',
      ].join('\n'),
      'templates/row.html':
        '{{define "cog.row.gear"}}<article class="card skinned">{{.Name}}</article>{{end}}',
    })
    server = await start(binary, { plugins: [p] })
    ui = await session(server)
  })

  after(async () => {
    await ui?.close()
    await server?.stop()
  })

  test('it is a fragment, never a document', async () => {
    const res = await ui.page.request.get(`${server.url}/workspaces/1/drawers/gears`)
    assert.equal(res.status(), 200)
    const html = await res.text()
    assert.ok(!/<html|doctype/i.test(html), 'a drawer came back wrapped in a document')
    assert.match(html, /dk-body/)
  })

  // Named rather than derived from the path: turning a URL segment into a
  // template name would let a request render anything the stack defines.
  test('a name nobody declared is a 404', async () => {
    const res = await ui.page.request.get(`${server.url}/workspaces/1/drawers/whatever`)
    assert.equal(res.status(), 404)
  })

  test('the application loads htmx, or no drawer would ever arrive', async () => {
    const res = await ui.page.request.get(`${server.url}/`)
    assert.match(await res.text(), /\/cog\/htmx\.min\.js/)
  })
})
