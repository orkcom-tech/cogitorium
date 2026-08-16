import type { Agent, Wire } from '../api'

// The blueprint arranges itself.
//
// Before this, an agent with no stored position went into a single row under
// the orchestrator: fine for two agents, and a straight line of eight that says
// nothing about which of them delegates to which. The graph IS the program in
// this product, so a layout that ignores the edges is a picture that leaves out
// the part worth seeing.
//
// This is a layered layout — rank by how far an agent is from a root, order
// within a rank to keep the wires from crossing, then place. It is the standard
// shape (Sugiyama) minus the parts that need a solver: no dummy nodes for edges
// that span several ranks, and barycentre ordering rather than an exact
// crossing minimisation, which is NP-hard and not worth it for graphs this size.
//
// Two properties it must have, and both are tested by driving the real canvas:
// it is DETERMINISTIC — the same workspace lays out identically every time, so
// an operator's mental map survives a reload — and a dragged position always
// wins, because the operator is the other hand on the same controls.

export type Pos = { x: number; y: number }

// The spacing. Wider than tall on purpose: a node is a card with a name and a
// model on it, so horizontal crowding is what actually makes a graph unreadable.
const COL = 260
const ROW = 190

/**
 * layered places every agent by its distance from a root and its neighbours.
 *
 * Roots are the orchestrators, or — for a workspace whose wires do not reach
 * everything — any agent nothing delegates to. An agent in neither category is
 * in a cycle, and gets the rank its first-seen predecessor gives it rather than
 * being dropped: a graph with a cycle in it is still a graph somebody has to
 * look at.
 */
export function layered(agents: Agent[], wires: Wire[]): Map<number, Pos> {
  const out = new Map<number, Pos>()
  if (agents.length === 0) return out

  const ids = agents.map((a) => a.id)
  const known = new Set(ids)
  // Edges that point at an agent which is no longer there would otherwise rank
  // nothing and silently drop a node off the canvas.
  const edges = wires.filter((w) => known.has(w.from_agent_id) && known.has(w.to_agent_id))

  const outgoing = new Map<number, number[]>()
  const incoming = new Map<number, number[]>()
  for (const id of ids) {
    outgoing.set(id, [])
    incoming.set(id, [])
  }
  for (const w of edges) {
    outgoing.get(w.from_agent_id)!.push(w.to_agent_id)
    incoming.get(w.to_agent_id)!.push(w.from_agent_id)
  }

  // 1. Rank. Longest path from a root, so an edge always points downward and a
  // worker two delegations deep sits below one that is one deep — even when it
  // is also reachable directly.
  const rank = new Map<number, number>()
  const roots = ids.filter((id) => {
    const a = agents.find((x) => x.id === id)!
    return a.is_orchestrator || incoming.get(id)!.length === 0
  })
  // A workspace whose every agent has an incoming wire (a cycle with no
  // orchestrator) still has to start somewhere; the lowest id is arbitrary but
  // stable, which is what matters.
  const seeds = roots.length > 0 ? roots : [Math.min(...ids)]
  for (const id of seeds) rank.set(id, 0)

  // Relaxation rather than recursion: it terminates on a cycle without a
  // visited-set dance, because a rank only ever increases and is capped.
  const cap = ids.length
  for (let pass = 0; pass < cap; pass++) {
    let moved = false
    for (const w of edges) {
      const from = rank.get(w.from_agent_id)
      if (from === undefined) continue
      const want = from + 1
      if (want > cap) continue // a cycle: stop deepening rather than run away
      const have = rank.get(w.to_agent_id)
      if (have === undefined || have < want) {
        rank.set(w.to_agent_id, want)
        moved = true
      }
    }
    if (!moved) break
  }
  // Anything the wires never reached — an agent nobody delegates to and which
  // delegates to nobody — goes on its own rank at the bottom rather than piling
  // up on rank 0 with the orchestrator.
  const maxRanked = Math.max(0, ...Array.from(rank.values()))
  for (const id of ids) if (!rank.has(id)) rank.set(id, maxRanked + 1)

  // 2. Order within each rank. Start from the agents' own order, which is the
  // database's, so the result is stable; then sweep by barycentre — put each
  // node near the average position of what it connects to on the rank above.
  const byRank = new Map<number, number[]>()
  for (const a of agents) {
    const r = rank.get(a.id)!
    if (!byRank.has(r)) byRank.set(r, [])
    byRank.get(r)!.push(a.id)
  }
  const ranks = Array.from(byRank.keys()).sort((a, b) => a - b)

  const indexIn = (list: number[], id: number) => list.indexOf(id)
  for (let sweep = 0; sweep < 4; sweep++) {
    for (let i = 1; i < ranks.length; i++) {
      const above = byRank.get(ranks[i - 1])!
      const here = byRank.get(ranks[i])!
      const key = new Map<number, number>()
      here.forEach((id, at) => {
        const parents = incoming.get(id)!.map((p) => indexIn(above, p)).filter((n) => n >= 0)
        // No parent on the rank above: keep where it is, rather than collapsing
        // every orphan onto the left edge.
        key.set(id, parents.length ? parents.reduce((s, n) => s + n, 0) / parents.length : at)
      })
      // A stable sort with the current index as the tie-break, so equal
      // barycentres never shuffle between renders.
      here.sort((p, q) => (key.get(p)! - key.get(q)!) || indexIn(here, p) - indexIn(here, q))
    }
  }

  // 3. Place. Each rank is centred on x=0, so adding an agent grows the graph
  // outwards rather than shifting everything already on screen sideways.
  for (const r of ranks) {
    const row = byRank.get(r)!
    row.forEach((id, i) => {
      out.set(id, { x: (i - (row.length - 1) / 2) * COL, y: r * ROW })
    })
  }
  return out
}

/**
 * positionsFor is what the canvas actually draws: a stored position wins, and
 * everything else is laid out.
 *
 * The two hands on the same controls, in one function. An operator who dragged
 * a node meant it; an agent the orchestrator hired a second ago has no opinion,
 * and putting it where the graph says it goes is better than putting it at the
 * origin under whatever is already there.
 */
export function positionsFor(agents: Agent[], wires: Wire[]): Map<number, Pos> {
  const auto = layered(agents, wires)
  const out = new Map<number, Pos>()
  for (const a of agents) {
    if (a.pos_x != null && a.pos_y != null) out.set(a.id, { x: a.pos_x, y: a.pos_y })
    else out.set(a.id, auto.get(a.id) ?? { x: 0, y: 0 })
  }
  return out
}
