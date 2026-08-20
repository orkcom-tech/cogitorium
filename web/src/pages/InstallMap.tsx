import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type GraphData, type Workspace } from '../api'
import { hueOf } from './hue'
import { usePublishShell } from '../shell'
import StageSlot from '../StageSlot'

/**
 * The install, as a map.
 *
 * Three depths in ONE scene, because zooming should approach rather than
 * navigate: the organisation and who is in it, the workspaces and who may
 * reach them, and — when you open one — what is actually inside it.
 *
 * TWO RULES DECIDE WHETHER THIS IS READABLE OR A HAIRBALL, and both were
 * arrived at by having the design attacked before it was built.
 *
 * POSITION ENCODES RELATION. A node sits in the angular sector of whatever it
 * is related to, so its links barely travel. A workspace granted to Core sits
 * in Core's wedge and its grant line is a short radial stub instead of a chord
 * across the whole canvas. Mush is not caused by having many edges; it is
 * caused by edges having to travel, which is a placement problem.
 *
 * ONLY KINDS THE SERVER ACTUALLY SENDS ARE DRAWN. /api/v1/map returns user,
 * team and workspace. /workspaces/{id}/graph returns agent, gear, shared,
 * private, document and instruction. There are no skills, gates or receivers
 * in either payload — an earlier draft of this drew all three, and a lane
 * that is permanently empty is not a neutral omission: it is a positive claim
 * that the install has no doors in and no outward addresses.
 */

type MapNode = { id: string; kind: string; label: string; detail?: string }
type MapEdge = { from: string; to: string; kind: string }
type MapData = { nodes: MapNode[]; edges: MapEdge[] }

type Placed = {
  id: string
  kind: string
  label: string
  detail?: string
  x: number
  y: number
  r: number
  hue: number
  level: 0 | 1 | 2
  /** the workspace this belongs to, for level 2 */
  ws?: string
  /** a per-particle phase, so the cloud drifts without every mote in step */
  drift?: number
  /** where the particle sits before the cloud is turned */
  bx?: number
  by?: number
  /** the bearing this workspace's tree grows along */
  angle?: number
}

type Link = { a: string; b: string; hue: number; kind: string; inside?: string; core?: boolean; dotted?: boolean }

const TAU = Math.PI * 2

export default function InstallMap() {
  // Only where we are. This used to publish a two-item switcher — Workspaces
  // and Map — which is now what the rail's own destinations offer on every
  // screen outside a workspace, so it drew the same two buttons twice, one
  // group above the other.
  usePublishShell(() => ({ here: { label: 'Map' } }), [])

  const [data, setData] = useState<MapData | null>(null)
  const [spaces, setSpaces] = useState<Workspace[]>([])
  const [error, setError] = useState<string | null>(null)
  const [openWs, setOpenWs] = useState<string | null>(null)
  // The install-wide things the core is made of. They are their own endpoints
  // rather than part of the map payload, because they are not relationships.
  const [extra, setExtra] = useState<{ gears: { id: number; name: string }[] }>({ gears: [] })
  const [inside, setInside] = useState<Record<string, GraphData>>({})
  const [focus, setFocus] = useState<Placed | null>(null)

  useEffect(() => {
    Promise.all([api.graph.map(), api.workspaces.list()])
      .then(([g, ws]) => {
        setData(g as MapData)
        setSpaces(ws)
      })
      .catch((e: Error) => setError(e.message))
    // Gears and people are fetched separately and are allowed to fail: a
    // member cannot list either, and the map is still worth drawing without
    // them. A core with fewer shells is honest; a map that refuses to load
    // because one optional shell 403'd is not.
    // Gears are their own endpoint and are allowed to fail: a member cannot
    // list them, and a core with one shell fewer is honest where a map that
    // refuses to load because an optional shell 403'd is not.
    //
    // People are NOT fetched separately. They are already in the map payload,
    // filtered server-side to whoever this caller may see, and asking a second
    // endpoint would be a second answer to the same question — the one that is
    // not permission-scoped.
    api.gears
      .list()
      .then((g) => setExtra((e) => ({ ...e, gears: g.map((x) => ({ id: x.id, name: x.name })) })))
      .catch(() => setExtra((e) => ({ ...e, gears: [] })))
  }, [])

  // A workspace's contents are fetched only when it is opened. Sixty
  // workspaces must not be sixty requests on load, and the map is legible
  // without them — that is the whole point of it having levels.
  const open = useCallback(
    (id: string) => {
      const wsId = Number(id.slice(2))
      setOpenWs(id)
      if (inside[id] || !Number.isFinite(wsId)) return
      api.graph
        .workspace(wsId)
        .then((g) => setInside((m) => ({ ...m, [id]: g })))
        .catch((e: Error) => setError(e.message))
    },
    [inside],
  )

  const layout = useMemo(() => layoutOf(data, spaces, extra, openWs, inside), [data, spaces, extra, openWs, inside])

  if (error) return <p className="error">{error}</p>
  if (!data) return <p className="hint">reading the install…</p>

  return (
    <div className="imap">
      <StageSlot screen="map" />
      <Canvas
        layout={layout}
        openWs={openWs}
        onPick={(n) => {
          setFocus(n)
          if (n?.kind === 'workspace') open(n.id)
          else if (!n) {
            setOpenWs(null)
            setFocus(null)
          }
        }}
      />
      {focus && (
        <aside className="imap-card">
          <h3>{focus.label}</h3>
          <p className="sub">{focus.detail || focus.kind}</p>
          {focus.kind === 'workspace' && !inside[focus.id] && <p className="hint">opening…</p>}
          {focus.kind === 'workspace' && inside[focus.id] && (
            <dl>
              <dt>agents</dt>
              <dd>{inside[focus.id].nodes.filter((n) => n.kind === 'agent').length}</dd>
              <dt>memory</dt>
              <dd>{inside[focus.id].nodes.filter((n) => n.kind === 'shared' || n.kind === 'private').length}</dd>
              <dt>links</dt>
              <dd>{inside[focus.id].edges.length}</dd>
            </dl>
          )}
          <button onClick={() => setFocus(null)}>close</button>
        </aside>
      )}
      <p className="imap-hint">
        {openWs ? 'click the ground to come back out' : 'click a workspace to open it · scroll to zoom · drag to pan'}
      </p>
    </div>
  )
}

/**
 * Where everything goes.
 *
 * Deterministic, so the same install draws the same map on every load — an
 * operator's memory of where a thing sits is the only reason a spatial view
 * beats a list, and a layout that moves throws that away.
 */
function layoutOf(
  data: MapData | null,
  spaces: Workspace[],
  extra: { gears: { id: number; name: string }[] },
  openWs: string | null,
  inside: Record<string, GraphData>,
): { nodes: Placed[]; links: Link[] } {
  if (!data) return { nodes: [], links: [] }

  const nodes: Placed[] = []
  const links: Link[] = []
  const at = new Map<string, Placed>()
  const push = (p: Placed) => {
    nodes.push(p)
    at.set(p.id, p)
    return p
  }

  const teams = data.nodes.filter((n) => n.kind === 'team')
  const works = data.nodes.filter((n) => n.kind === 'workspace')

  // ---- THE CORE, IN SHELLS -------------------------------------------------
  //
  // People are the innermost thing an install has: everything else exists
  // because somebody made it. Outward from them, in order of how shared a
  // thing is — teams, then the tools and instructions the whole install can
  // reach. Each shell is a ring of drifting particles rather than a diagram,
  // because at the distance you normally look at this, a diagram of forty
  // small things is a smudge with lines through it. The lines are still there;
  // they appear when you come close enough for them to mean something.
  const shell = (items: { id: string; label: string; kind: string }[], radius: number, hue: number, r: number) => {
    items.forEach((it, i) => {
      // Golden-angle placement, so a shell of any size fills evenly instead of
      // leaving a seam where the first and last item meet.
      const a = i * 2.399963 + radius
      const rr = radius * (0.72 + ((i * 0.618) % 1) * 0.42)
      push({
        id: it.id, kind: it.kind, label: it.label,
        x: Math.cos(a) * rr, y: Math.sin(a) * rr * 0.9,
        r, hue, level: 0, drift: (i * 0.618) % 1,
      })
    })
  }

  const people = data.nodes.filter((n) => n.kind === 'user')
  shell(people.map((u) => ({ id: u.id, label: u.label, kind: 'user' })), 70, 210, 6)
  shell(teams.map((t) => ({ id: t.id, label: t.label, kind: 'team' })), 150, 275, 7)
  shell(extra.gears.map((g) => ({ id: `g-${g.id}`, label: g.name, kind: 'gear' })), 240, 150, 4.5)

  // Membership is a real edge and it is drawn — but only from close up. See
  // the link rule in the canvas.
  data.edges.filter((e) => e.kind === 'member').forEach((e) => {
    if (at.has(e.from) && at.has(e.to)) links.push({ a: e.from, b: e.to, hue: 275, kind: 'member', core: true })
  })

  // ---- THE BRANCHES --------------------------------------------------------
  //
  // Workspaces sit well outside the core, each on its own bearing, and each
  // grows a tree outward: the orchestrator, its agents, and what they run. The
  // gap between the core and the ring is deliberate — it is the room a tree
  // needs when it opens, reserved at layout time so opening one fills empty
  // space instead of shoving its neighbours aside.
  const R_WS = 620

  works.forEach((w, i) => {
    const a = (i / Math.max(1, works.length)) * TAU - Math.PI / 2
    const id = Number(w.id.slice(2))
    const ws = spaces.find((x) => x.id === id)
    const hue = ws ? hueOf(ws) : 150
    const n = push({
      id: w.id, kind: 'workspace', label: w.label, detail: w.detail,
      x: Math.cos(a) * R_WS, y: Math.sin(a) * R_WS * 0.86, r: 18, hue, level: 1, angle: a,
    })
    // A dotted reach into the middle, so "this shares the install's context"
    // is visible without a line per document.
    links.push({ a: 'core', b: n.id, hue, kind: 'reaches', dotted: true })

    const g = openWs === w.id ? inside[w.id] : undefined
    if (!g) return

    const agents = g.nodes.filter((x) => x.kind === 'agent')
    const tools = g.nodes.filter((x) => x.kind === 'gear' || x.kind === 'instruction' || x.kind === 'document')
    const mem = g.nodes.filter((x) => x.kind === 'shared' || x.kind === 'private')

    const branch = (items: typeof g.nodes, base: number, spread: number, size: number) => {
      // The radius carries a capacity term. A flat fan of thirty workers on a
      // fixed ring overlaps every neighbour, and a flat fan is this product's
      // canonical shape — one orchestrator, N workers, all at depth one.
      const r = Math.max(base, (items.length * 44) / TAU)
      items.forEach((item, j) => {
        const ang = a + ((j - (items.length - 1) / 2) / Math.max(1, items.length)) * spread
        push({
          id: item.id, kind: item.kind, label: item.label, detail: item.detail,
          x: n.x + Math.cos(ang) * r, y: n.y + Math.sin(ang) * r,
          r: size, hue: KIND_HUE[item.kind] ?? hue, level: 2, ws: w.id,
        })
      })
    }
    branch(agents, 130, 1.5, 7)
    branch(tools, 250, 1.9, 5)
    // Memory and context sit furthest out: they are what an agent reaches for
    // rather than what it is, and they only earn their space up close.
    branch(mem, 350, 1.7, 5)

    g.edges.forEach((e) => {
      if (at.has(e.from) && at.has(e.to)) {
        links.push({ a: e.from, b: e.to, hue: KIND_HUE[e.kind] ?? hue, kind: e.kind, inside: w.id })
      }
    })
    // The orchestrator is joined to its hub, and so is anything the payload
    // left unlinked — a capability granted to the whole workspace is the
    // broadest one there is, and drawing it as a loose dot is the opposite of
    // the truth.
    const touched = new Set(g.edges.flatMap((e) => [e.from, e.to]))
    g.nodes.filter((x) => !touched.has(x.id)).forEach((x) => {
      links.push({ a: w.id, b: x.id, hue: KIND_HUE[x.kind] ?? hue, kind: 'all', inside: w.id })
    })
    const orc = agents[0]
    if (orc) links.push({ a: w.id, b: orc.id, hue, kind: 'runs', inside: w.id })
  })

  // The core itself is a node so the branches have something to reach toward,
  // but it is drawn as a glow rather than a circle: it is a place, not a thing.
  push({ id: 'core', kind: 'core', label: 'this install', x: 0, y: 0, r: 10, hue: 150, level: 0 })

  return { nodes, links }
}

// Only kinds the server actually sends. See the note at the top of the file.
const KIND_HUE: Record<string, number> = {
  agent: 210,
  gear: 150,
  shared: 275,
  private: 300,
  document: 40,
  instruction: 95,
  delegates: 210,
  knows: 275,
  tool: 150,
}

/**
 * The canvas.
 *
 * Drawn rather than laid out in the DOM because the whole install is one
 * scene: at fifteen nodes React Flow would be fine, and at fifteen hundred it
 * is a React component per node. The map has to survive the second case to be
 * worth building at all.
 *
 * The camera is one eased tween that every movement goes through, so "click a
 * thing and it comes to you" is a single code path rather than an animation
 * written per target.
 */
function Canvas({
  layout,
  openWs,
  onPick,
}: {
  layout: { nodes: Placed[]; links: Link[] }
  openWs: string | null
  onPick: (n: Placed | null) => void
}) {
  const ref = useRef<HTMLCanvasElement>(null)
  const cam = useRef({ x: 0, y: 0, z: 0.62 })
  const tween = useRef<{ from: { x: number; y: number; z: number }; to: { x: number; y: number; z: number }; at: number } | null>(null)
  const hover = useRef<string | null>(null)
  // The scene is read through a ref so a new layout does not tear down the
  // canvas, its listeners and the camera position along with it.
  const scene = useRef(layout)
  scene.current = layout
  const pickRef = useRef(onPick)
  pickRef.current = onPick
  const openRef = useRef(openWs)
  openRef.current = openWs

  // The blueprint eases outward from its hub on ONE number, read by both the
  // position and the alpha, so an expansion can never half-happen.
  const grow = useRef(0)

  useEffect(() => {
    const cv = ref.current
    if (!cv) return
    const ctx = cv.getContext('2d')
    if (!ctx) return
    let raf = 0
    let w = 0
    let h = 0
    const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches

    const fit = () => {
      const dpr = Math.min(2, devicePixelRatio || 1)
      const box = cv.getBoundingClientRect()
      w = box.width
      h = box.height
      cv.width = Math.round(w * dpr)
      cv.height = Math.round(h * dpr)
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    }
    fit()
    const ro = new ResizeObserver(fit)
    ro.observe(cv)

    const css = getComputedStyle(document.documentElement)

    /**
     * A token, resolved to something canvas can actually parse.
     *
     * getPropertyValue on a custom property returns the token as WRITTEN, and
     * every colour in this product is written as light-dark(a, b). Canvas
     * cannot parse that, and assigning an unparseable value to fillStyle is
     * not an error — it is silently ignored, so the context keeps whatever
     * colour it had last. The symptom is not a missing colour: it is every
     * node on the map drawn in the first colour that happened to parse.
     *
     * The browser will resolve it if asked properly, so it is asked: set the
     * value on a real element and read the computed result back.
     */
    const probe = document.createElement('span')
    probe.style.display = 'none'
    cv.parentElement?.appendChild(probe)
    let resolved = new Map<string, string>()
    const token = (name: string, fallback: string) => {
      const hit = resolved.get(name)
      if (hit) return hit
      probe.style.color = `var(${name})`
      const v = getComputedStyle(probe).color || fallback
      resolved.set(name, v)
      return v
    }
    // The cache has to be thrown away when the look or the mode changes.
    // Without this it is resolved once, on mount, and never again: switching to
    // dark afterwards left every label painted in the light theme's near-black
    // ink on a near-black ground, so the map appeared to have no labels at all
    // rather than unreadable ones.
    const watch = new MutationObserver(() => {
      resolved = new Map()
    })
    watch.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'data-look'] })

    const draw = (now: number) => {
      const c = cam.current
      const t = tween.current
      if (t) {
        const k = Math.min(1, (now - t.at) / 700)
        const e = 1 - Math.pow(1 - k, 4)
        c.x = t.from.x + (t.to.x - t.from.x) * e
        c.y = t.from.y + (t.to.y - t.from.y) * e
        c.z = t.from.z + (t.to.z - t.from.z) * e
        if (k >= 1) tween.current = null
      }
      grow.current += ((openRef.current ? 1 : 0) - grow.current) * (reduce ? 1 : 0.12)

      const sx = (x: number) => (x - c.x) * c.z + w / 2
      const sy = (y: number) => (y - c.y) * c.z + h / 2

      ctx.clearRect(0, 0, w, h)

      const { nodes, links } = scene.current
      const by = new Map(nodes.map((n) => [n.id, n]))
      const live = (n: Placed) => (n.level === 2 ? grow.current : 1)
      const open = openRef.current
      const secs = now / 1000

      // The core turns, slowly, and each particle wanders on its own phase.
      // This is the only thing on the map that moves by itself, and it is the
      // only thing that is genuinely always in use.
      const spin = secs * 0.021
      for (const n of nodes) {
        if (n.drift === undefined) continue
        if (n.bx === undefined) {
          n.bx = n.x
          n.by = n.y
        }
        const bx = n.bx ?? 0
        const byy = n.by ?? 0
        const rot = Math.atan2(byy, bx) + spin
        const rad = Math.hypot(bx, byy)
        n.x = Math.cos(rot) * rad + Math.sin(secs * 0.4 + n.drift * 9) * 4
        n.y = Math.sin(rot) * rad * 0.9 + Math.cos(secs * 0.36 + n.drift * 11) * 3.5
      }

      // LINKS ARE A CLOSE-UP DETAIL.
      //
      // At map scale the core is forty small things and the lines between them
      // are a grey haze that hides the very particles they connect. So they
      // fade in with the zoom rather than being drawn always: far out the core
      // is a cloud that drifts, and the relationships appear as you approach
      // the point where a single one of them could be followed.
      const linkLOD = Math.max(0, Math.min(1, (c.z - 0.55) / 0.45))

      ctx.lineCap = 'round'
      for (const l of links) {
        const a = by.get(l.a)
        const b = by.get(l.b)
        if (!a || !b) continue
        // A reach into the middle stays visible at every distance: it is the
        // one relationship the map exists to state. Everything else is detail.
        const base = l.dotted ? 0.22 : l.inside ? 0.55 * grow.current : linkLOD * (open ? 0.14 : 0.4)
        const alpha = base * Math.min(live(a), live(b))
        if (alpha < 0.02) continue
        ctx.strokeStyle = `hsl(${l.hue} 55% 50% / ${alpha})`
        ctx.lineWidth = Math.max(0.6, 1.2 * c.z)
        if (l.dotted) {
          ctx.setLineDash([2, 7])
          ctx.lineDashOffset = -(secs * 10) % 9
        } else ctx.setLineDash([])
        ctx.beginPath()
        ctx.moveTo(sx(a.x), sy(a.y))
        ctx.lineTo(sx(b.x), sy(b.y))
        ctx.stroke()
        ctx.setLineDash([])
      }


      const ink = token('--text', '#1e1e1e')
      const wants: { n: Placed; X: number; Y: number; r: number; alpha: number; on: boolean }[] = []
      for (const n of nodes) {
        const a = live(n)
        if (a < 0.02) continue
        const X = sx(n.x)
        const Y = sy(n.y)
        if (X < -80 || X > w + 80 || Y < -80 || Y > h + 80) continue
        const on = hover.current === n.id
        const r = Math.max(2, n.r * c.z * (n.level === 2 ? 0.4 + 0.6 * grow.current : 1) * (on ? 1.3 : 1))
        const dim = open && n.level < 2 && n.id !== open ? 0.3 : 1

        ctx.globalAlpha = a * dim
        if (n.kind === 'core') {
          // A place rather than a thing: everything in the middle belongs to
          // the install, and a circle round it would make the install look
          // like one more node among its own contents.
          const g = ctx.createRadialGradient(X, Y, 0, X, Y, 150 * c.z)
          g.addColorStop(0, `hsl(${n.hue} 60% 55% / 0.22)`)
          g.addColorStop(0.5, `hsl(${n.hue} 60% 55% / 0.07)`)
          g.addColorStop(1, `hsl(${n.hue} 60% 55% / 0)`)
          ctx.fillStyle = g
          ctx.beginPath()
          ctx.arc(X, Y, 150 * c.z, 0, TAU)
          ctx.fill()
          ctx.globalAlpha = 1
          continue
        }

        // EVERY NODE IS A LIT BODY, NOT AN OUTLINE.
        //
        // The first cut filled hubs with the surface colour and stroked them,
        // which in a light theme reads as a neat white bead and in a dark one
        // reads as a hole: the fill was the same near-black as the ground, so
        // the only thing drawn was a thin ring around nothing. Worse, with
        // every node the same weight the whole map was flat — five identical
        // outlines and a scatter of dots, with nothing saying which of them
        // was the thing you came to look at.
        //
        // So the colour is the body's own, the halo carries its importance,
        // and a hub keeps a bright rim on top for the crispness the outline
        // was there for in the first place.
        const halo = n.kind === 'workspace' ? 3.4 : n.kind === 'team' ? 2.6 : 2
        const glow = ctx.createRadialGradient(X, Y, 0, X, Y, r * halo)
        glow.addColorStop(0, `hsl(${n.hue} 70% 55% / ${n.level === 1 ? 0.4 : 0.24})`)
        glow.addColorStop(1, `hsl(${n.hue} 70% 55% / 0)`)
        ctx.fillStyle = glow
        ctx.beginPath()
        ctx.arc(X, Y, r * halo, 0, TAU)
        ctx.fill()

        ctx.fillStyle = `hsl(${n.hue} ${n.level === 1 ? 62 : 55}% ${n.level === 1 ? 52 : 58}%)`
        ctx.beginPath()
        ctx.arc(X, Y, r, 0, TAU)
        ctx.fill()

        if (n.kind === 'workspace' || n.kind === 'team') {
          // A rim, lighter than the body, so a hub still has an edge to catch
          // at any size without going hollow.
          ctx.strokeStyle = `hsl(${n.hue} 75% 72% / 0.9)`
          ctx.lineWidth = Math.max(1, 1.6 * c.z)
          ctx.beginPath()
          ctx.arc(X, Y, r, 0, TAU)
          ctx.stroke()
        }

        // A name appears once there is room to read it; below that zoom the
        // labels are a grey texture rather than words. They are COLLECTED here
        // and drawn after every node, so a name is never painted under a body,
        // and so the ones that collide can be resolved against each other.
        if (c.z > 0.5 || on || n.kind === 'workspace' || n.kind === 'org') {
          wants.push({ n, X, Y, r, alpha: a * dim * (on ? 1 : 0.86), on })
        }
        ctx.globalAlpha = 1
      }

      // --- names, placed so that no two overlap -----------------------------
      //
      // Every label claims a box. One that cannot find clear space is dropped,
      // and that is the right trade: a missing name is recoverable — the node
      // is still there and still hoverable — while two names written over each
      // other are not, because neither reads and neither can be pointed at.
      //
      // Order is by importance, so when space runs out it is the least
      // important name that goes. A hovered node always wins: it is the one
      // the operator is actually asking about.
      const rank = (n: Placed) =>
        n.kind === 'workspace' ? 0 : n.kind === 'team' ? 1 : n.kind === 'user' ? 2 : n.kind === 'agent' ? 3 : 4
      wants.sort((p, q) => (q.on ? 1 : 0) - (p.on ? 1 : 0) || rank(p.n) - rank(q.n))

      const claimed: { x: number; y: number; w: number; h: number }[] = []
      const bg = token('--bg', '#ffffff')
      for (const want of wants) {
        const { n, X, Y, r, alpha, on } = want
        ctx.font = `${n.kind === 'workspace' ? 600 : 400} ${Math.max(10, 11 * Math.min(1.4, c.z))}px ${css.fontFamily}`
        const tw = ctx.measureText(n.label).width
        // Above first, then below, then to either side: a name belongs over its
        // node, and the alternatives are tried in the order that keeps it
        // nearest to the thing it names.
        const spots: [number, number, CanvasTextAlign][] = [
          [X, Y - r - 7, 'center'],
          [X, Y + r + 15, 'center'],
          [X + r + 7, Y + 4, 'left'],
          [X - r - 7, Y + 4, 'right'],
        ]
        let put: [number, number, CanvasTextAlign] | null = null
        for (const [tx, ty, align] of spots) {
          const left = align === 'center' ? tx - tw / 2 : align === 'left' ? tx : tx - tw
          const box = { x: left - 2, y: ty - 10, w: tw + 4, h: 14 }
          if (
            claimed.some(
              (b) => box.x < b.x + b.w && box.x + box.w > b.x && box.y < b.y + b.h && box.y + box.h > b.y,
            )
          ) {
            continue
          }
          claimed.push(box)
          put = [tx, ty, align]
          break
        }
        // A hovered label is drawn even where nothing was free: it is the
        // answer to a direct question, and answering it is worth briefly
        // covering a neighbour.
        if (!put && on) put = [X, Y - r - 7, 'center']
        if (!put) continue

        const [tx, ty, align] = put
        ctx.globalAlpha = alpha
        ctx.textAlign = align
        // A stroke of the GROUND behind the ink, so a name stays readable
        // where it crosses a link or another node's halo. The ground rather
        // than a plate: a plate on every label turns a map into a list of
        // stickers.
        ctx.lineWidth = 3
        ctx.lineJoin = 'round'
        ctx.strokeStyle = bg
        ctx.strokeText(n.label, tx, ty)
        ctx.fillStyle = ink
        ctx.fillText(n.label, tx, ty)
        ctx.textAlign = 'left'
        ctx.globalAlpha = 1
      }

      raf = requestAnimationFrame(draw)
    }
    raf = requestAnimationFrame(draw)

    const pick = (px: number, py: number): Placed | null => {
      const c = cam.current
      let best: Placed | null = null
      let bd = Infinity
      for (const n of scene.current.nodes) {
        if (n.level === 2 && grow.current < 0.5) continue
        const X = (n.x - c.x) * c.z + w / 2
        const Y = (n.y - c.y) * c.z + h / 2
        const d = Math.hypot(px - X, py - Y)
        const hit = Math.max(12, n.r * c.z + 8)
        if (d < hit && d < bd) {
          best = n
          bd = d
        }
      }
      return best
    }

    let down: { x: number; y: number; cx: number; cy: number } | null = null
    let moved = 0

    const onDown = (e: PointerEvent) => {
      down = { x: e.clientX, y: e.clientY, cx: cam.current.x, cy: cam.current.y }
      moved = 0
      cv.setPointerCapture(e.pointerId)
    }
    const onMove = (e: PointerEvent) => {
      const box = cv.getBoundingClientRect()
      if (down) {
        moved = Math.max(moved, Math.hypot(e.clientX - down.x, e.clientY - down.y))
        // A drag is never a click, or every pan ends by zooming into whatever
        // happened to be under the finger when it stopped.
        if (moved > 3) {
          tween.current = null
          cam.current.x = down.cx - (e.clientX - down.x) / cam.current.z
          cam.current.y = down.cy - (e.clientY - down.y) / cam.current.z
        }
        return
      }
      const hit = pick(e.clientX - box.left, e.clientY - box.top)
      hover.current = hit?.id ?? null
      cv.style.cursor = hit ? 'pointer' : 'grab'
    }
    const onUp = (e: PointerEvent) => {
      cv.releasePointerCapture(e.pointerId)
      const wasDrag = moved > 3
      down = null
      if (wasDrag) return
      const box = cv.getBoundingClientRect()
      const hit = pick(e.clientX - box.left, e.clientY - box.top)
      pickRef.current(hit)
      tween.current = {
        from: { ...cam.current },
        to: hit
          ? { x: hit.x, y: hit.y, z: hit.kind === 'workspace' ? 1.05 : Math.max(cam.current.z, 0.85) }
          : { x: 0, y: 0, z: 0.62 },
        at: performance.now(),
      }
    }
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      tween.current = null
      const box = cv.getBoundingClientRect()
      const c = cam.current
      const bx = (e.clientX - box.left - w / 2) / c.z + c.x
      const byy = (e.clientY - box.top - h / 2) / c.z + c.y
      c.z = Math.max(0.2, Math.min(4, c.z * (e.deltaY < 0 ? 1.12 : 0.89)))
      // Keep the point under the pointer under the pointer — the one thing
      // that makes wheel zoom feel like a camera rather than a slider.
      c.x = bx - (e.clientX - box.left - w / 2) / c.z
      c.y = byy - (e.clientY - box.top - h / 2) / c.z
    }

    cv.addEventListener('pointerdown', onDown)
    cv.addEventListener('pointermove', onMove)
    cv.addEventListener('pointerup', onUp)
    cv.addEventListener('wheel', onWheel, { passive: false })

    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
      watch.disconnect()
      probe.remove()
      cv.removeEventListener('pointerdown', onDown)
      cv.removeEventListener('pointermove', onMove)
      cv.removeEventListener('pointerup', onUp)
      cv.removeEventListener('wheel', onWheel)
    }
  }, [])

  return <canvas className="imap-canvas" ref={ref} />
}
