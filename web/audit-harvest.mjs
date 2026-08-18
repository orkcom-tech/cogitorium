import { chromium } from 'playwright'
import fs from 'fs'

const HARVEST = () => {
  const out = []
  const sel = 'button, input, select, textarea, a, [role=button], [role=tab], [role=switch], [contenteditable="true"], label'
  const els = Array.from(document.querySelectorAll(sel))
  for (const e of els) {
    const r = e.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) continue
    const cs = getComputedStyle(e)
    const txt = (e.getAttribute('aria-label') || e.textContent || e.getAttribute('placeholder') || e.value || '').trim().slice(0, 40)
    // path
    let p = [], n = e, depth=0
    while (n && n.nodeType === 1 && depth < 4) {
      let s = n.tagName.toLowerCase()
      if (n.className && typeof n.className === 'string') s += '.' + n.className.trim().split(/\s+/).slice(0,3).join('.')
      p.unshift(s); n = n.parentElement; depth++
    }
    out.push({
      tag: e.tagName.toLowerCase(),
      type: e.getAttribute('type') || '',
      txt,
      w: Math.round(r.width*10)/10, h: Math.round(r.height*10)/10,
      x: Math.round(r.x), y: Math.round(r.y),
      radius: cs.borderRadius,
      pad: cs.padding,
      fs: cs.fontSize, fw: cs.fontWeight,
      ff: cs.fontFamily.split(',')[0],
      bg: cs.backgroundColor,
      border: cs.border,
      color: cs.color,
      tt: cs.textTransform,
      ls: cs.letterSpacing,
      path: p.join(' > ')
    })
  }
  return out
}

const b = await chromium.launch()
const ctx = await b.newContext({ viewport: { width: 1440, height: 900 } })
const p = await ctx.newPage()
const results = {}

async function grab(name, url, prep) {
  try {
    await p.goto(url, { waitUntil: 'networkidle', timeout: 20000 })
  } catch (e) { }
  await p.waitForTimeout(1200)
  if (prep) { try { await prep(p) } catch (e) { results[name+'__PREPERR'] = String(e).slice(0,200) } }
  await p.waitForTimeout(900)
  results[name] = await p.evaluate(HARVEST)
  await p.screenshot({ path: `/private/tmp/claude-501/-Users-eduardlugovtsov-Documents-Immaterium/4843ed60-79c7-4c92-bc19-4f63f3d521b9/scratchpad/shot-${name}.png`, fullPage: false })
}

const B = 'http://127.0.0.1:8896'
await grab('workspaces', B + '/workspaces')
await grab('map', B + '/map')
await grab('people', B + '/people')
await grab('models', B + '/models')
await grab('gears', B + '/gears')
await grab('instructions', B + '/instructions')
await grab('context', B + '/context')
await grab('terminal', B + '/terminal')
await grab('ws1-chat', B + '/workspaces/1')

const stages = ['Blueprint', 'Editor', 'Chat']
for (const s of stages) {
  await grab('ws1-stage-' + s, B + '/workspaces/1', async (pg) => {
    await pg.getByRole('button', { name: s, exact: true }).click()
  })
}

const drawers = ['Agents','Gears','Instructions','Memory','Receivers','Queue','Variables','Terminal']
for (const d of drawers) {
  await grab('ws1-drawer-' + d, B + '/workspaces/1', async (pg) => {
    await pg.getByRole('button', { name: d, exact: true }).click()
  })
}
await grab('ws4-drawer-Receivers', B + '/workspaces/4', async (pg) => {
  await pg.getByRole('button', { name: 'Receivers', exact: true }).click()
})
await grab('ws5-drawer-Variables', B + '/workspaces/5', async (pg) => {
  await pg.getByRole('button', { name: 'Variables', exact: true }).click()
})
await grab('ws5-blueprint', B + '/workspaces/5', async (pg) => {
  await pg.getByRole('button', { name: 'Blueprint', exact: true }).click()
})

fs.writeFileSync('/private/tmp/claude-501/-Users-eduardlugovtsov-Documents-Immaterium/4843ed60-79c7-4c92-bc19-4f63f3d521b9/scratchpad/harvest.json', JSON.stringify(results, null, 1))
console.log(Object.entries(results).map(([k,v]) => k + ': ' + (Array.isArray(v)? v.length : v)).join('\n'))
await b.close()
