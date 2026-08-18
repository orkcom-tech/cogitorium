import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent } from 'react'
import { Select } from './Select'
import { DropMenu } from './DropMenu'
import {
  Background,
  Controls,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Connection,
  type Edge,
  type Node,
  type NodeChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import {
  api,
  type Agent,
  type AgentStatus,
  type Gear,
  type GearBinding,
  type GraphData,
  type GraphNode,
  type EgressGrant,
  type ContextBinding,
  type Model,
  type Wire,
} from '../api'
import { draggedKind, readDragged, type Dragged } from '../dnd'
import { KINDS } from './GraphCanvas'
import WireEdge from './WireEdge'
import { layered, positionsFor } from './layout'

// Declared once, outside the component: React Flow warns and re-renders every
// edge if this object is a new identity on each pass.
const edgeTypes = { wire: WireEdge }

// Node and edge ids are namespaced because the canvas mixes two kinds of
// each: agents and gears, delegation wires and gear bindings.
const agentNode = (id: number) => `a-${id}`
const gearNode = (id: number) => `g-${id}`
const wireEdge = (id: number) => `w-${id}`
const bindingEdge = (id: number) => `b-${id}`
const idOf = (nodeOrEdgeId: string) => Number(nodeOrEdgeId.slice(2))

type NodeData = {
  kind: 'agent' | 'gear' | 'memory' | 'outward'
  egressOn?: boolean
  destination?: string
  agent?: Agent
  gear?: Gear
  memory?: GraphNode
  workspaceWide?: boolean
  state: string
}

// The memory layer answers a different question from the wiring layer — not
// "who may call whom" but "what does this agent know" — so it is a layer on
// the same canvas rather than a second graph to cross-reference.
const MEMORY_KINDS = new Set(['shared', 'private', 'document', 'instruction'])

const LAYER_HINT = {
  delegation: 'Wires: which agent may hand work to which',
  tools: 'Gears: which tools each agent may call',
  memory: 'Memory and context: what each agent knows',
  outward: 'The internet gate: which agents may ask to search the web',
}

// The one node that is not part of this workspace. Wiring an agent to it IS
// the grant — the same rule the delegation wires already follow, where the
// wire is the capability rather than a picture of one.
const OUTWARD = 'outward'
const egressEdge = (id: number) => `x-${id}`

export default function BlueprintEditor({
  wsId,
  agents,
  models,
  statuses,
  onChanged,
  onSelectAgent,
  onReviewGear,
  onError,
}: {
  wsId: number
  agents: Agent[]
  /** The catalog, so a new node can be given something to think with. */
  models: Model[]
  statuses: Map<number, AgentStatus>
  onChanged: () => void
  onSelectAgent: (a: Agent) => void
  /** Open a gear's review, wherever gears are shown. Used by the note a drop
   *  leaves behind when the gear that landed is not approved. */
  onReviewGear: (gearId: number) => void
  onError: (msg: string) => void
}) {
  const [adding, setAdding] = useState(false)
  const [wires, setWires] = useState<Wire[]>([])
  const [bindings, setBindings] = useState<GearBinding[]>([])
  const [catalog, setCatalog] = useState<Gear[]>([])
  const [graph, setGraph] = useState<GraphData | null>(null)
  // Wiring is what the operator came here to edit, so it starts visible;
  // memory is context for that work and is opened when it is wanted.
  // outward defaults ON: a capability that reaches off the machine must not
  // hide behind a toggle the operator has to remember to switch on.
  const [layers, setLayers] = useState({ delegation: true, tools: true, memory: false, outward: true })
  const [showHelp, setShowHelp] = useState(() => localStorage.getItem('cogitorium.bpHelp') !== 'off')
  const [egress, setEgress] = useState<{ enabled: boolean; destination: string; grants: EgressGrant[]; reach: Record<string, string[]> } | null>(null)
  const [nodes, setNodes, onNodesChange] = useNodesState<Node<NodeData>>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])
  // What the canvas already knows, so a drop can say "it already has that"
  // rather than asking the server and reporting a constraint violation.
  const [context, setContext] = useState<ContextBinding[]>([])
  // A drag is over the canvas. `over` is the agent under the pointer, or null
  // for the empty canvas — which is a real target, not the absence of one: it
  // means the whole workspace.
  const [drag, setDrag] = useState<{ kind: Dragged['kind']; over: number | null } | null>(null)
  // What the last drop did. It says the sentence the operator just performed,
  // because a node quietly appearing in a graph of twenty is not feedback.
  const [landed, setLanded] = useState<{ text: string; warn?: string; gearId?: number } | null>(null)
  // Narrowed to the one method used. The full ReactFlowInstance is generic in
  // the node type, and the nodes handed to <ReactFlow> are a mapped copy with
  // a rendered label in them — a different type from ours, so the instance
  // handed back does not match a ref declared with ours.
  const flow = useRef<{
    fitView: (o?: { padding?: number }) => void
    getNodes: () => { measured?: { width?: number } }[]
  } | null>(null)
  // The operator has panned, zoomed or moved a node: from then on the view is
  // theirs and nothing refits it under them.
  const owned = useRef(false)
  const lastFit = useRef('')
  const flowBox = useRef<HTMLDivElement>(null)

  const reloadGraph = useCallback(
    () =>
      Promise.all([
        api.wires.list(wsId),
        api.gears.bindings(wsId),
        api.gears.list(),
        api.graph.workspace(wsId),
        api.egress.grants(wsId),
        api.context.bindings(wsId),
      ])
        .then(([w, b, c, g, e, cb]) => {
          setWires(w)
          setBindings(b)
          setCatalog(c)
          setGraph(g)
          setEgress(e)
          setContext(cb)
        })
        .catch((e: Error) => onError(e.message)),
    [wsId, onError],
  )

  // Resync whenever the agent set changes — the orchestrator may have wired
  // agents or forged a gear mid-turn.
  useEffect(() => {
    void reloadGraph()
  }, [reloadGraph, agents])

  const gearById = useMemo(() => new Map(catalog.map((g) => [g.id, g])), [catalog])
  // A stored position wins; everything else is laid out by the wires. See
  // layout.ts — the old version put every unplaced agent in one row, which for
  // eight of them was a straight line that said nothing about who delegates to
  // whom.
  const positions = useMemo(() => positionsFor(agents, wires), [agents, wires])

  // Nodes rebuild only on structural change; statuses are applied in place
  // below so a live turn cannot snap a dragged node back.
  useEffect(() => {
    const agentNodes: Node<NodeData>[] = agents.map((a) => ({
      id: agentNode(a.id),
      position: positions.get(a.id) ?? { x: 0, y: 0 },
      data: { kind: 'agent', agent: a, state: 'idle' },
      className: `bp-node ${a.is_orchestrator ? 'bp-orchestrator' : ''} bp-idle`,
    }))

    // One node per distinct gear present in this workspace, regardless of
    // how many agents it is bound to.
    const gearIds = layers.tools ? [...new Set(bindings.map((b) => b.gear_id))] : []
    const gearNodes: Node<NodeData>[] = gearIds.map((gid, i) => {
      const g = gearById.get(gid)
      const wide = bindings.some((b) => b.gear_id === gid && b.agent_id === null)
      return {
        id: gearNode(gid),
        position: { x: (i - (gearIds.length - 1) / 2) * 200, y: 440 },
        data: { kind: 'gear', gear: g, workspaceWide: wide, state: 'idle' },
        className: `bp-node bp-gear bp-gear-${g?.status ?? 'pending'}`,
      }
    })

    // Memory sits above the agents it feeds, on the opposite side of the
    // canvas from the gears, so the two layers never fight for the same space.
    const memoryNodes: Node<NodeData>[] = !layers.memory
      ? []
      : (graph?.nodes ?? [])
          .filter((n) => MEMORY_KINDS.has(n.kind))
          .map((n, i, all) => ({
            id: n.id,
            position: { x: (i - (all.length - 1) / 2) * 230, y: -260 },
            data: { kind: 'memory', memory: n, state: 'idle' },
            className: `bp-node bp-memory ${KINDS[n.kind]?.className ?? ''}`,
          }))

    // One singleton node, pinned above the agents. When the master switch is
    // off no edges are drawn even where grants exist: "nobody can go out" and
    // "nobody is allowed to ask" are different facts and must look different.
    const outwardNodes: Node<NodeData>[] = !layers.outward
      ? []
      : [
          {
            id: OUTWARD,
            position: { x: 0, y: -420 },
            data: { kind: 'outward', state: 'idle', egressOn: egress?.enabled ?? false, destination: egress?.destination ?? '' },
            className: `bp-node bp-outward ${egress?.enabled ? '' : 'bp-outward-off'}`,
            draggable: false,
          },
        ]

    setNodes([...agentNodes, ...gearNodes, ...memoryNodes, ...outwardNodes])
  }, [agents, positions, bindings, gearById, graph, layers.tools, layers.memory, layers.outward, egress, setNodes])

  useEffect(() => {
    setNodes((prev) =>
      prev.map((n) => {
        if (n.data.kind !== 'agent' || !n.data.agent) return n
        const a = n.data.agent
        const state = statuses.get(a.id)?.state ?? 'idle'
        if (n.data.state === state) return n
        return {
          ...n,
          data: { ...n.data, state },
          className: `bp-node ${a.is_orchestrator ? 'bp-orchestrator' : ''} bp-${state}`,
        }
      }),
    )
  }, [statuses, setNodes])

  useEffect(() => {
    // Two crowds, counted separately: wires leaving one agent, and wires
    // arriving at one. WireEdge spreads the first along the curve and the
    // second across the bundle, and neither can be worked out from the edge
    // alone — an edge only knows about itself.
    const fanOut = new Map<number, number[]>()
    const fanIn = new Map<number, number[]>()
    for (const w of wires) {
      const out = fanOut.get(w.from_agent_id) ?? []
      out.push(w.id)
      fanOut.set(w.from_agent_id, out)
      const into = fanIn.get(w.to_agent_id) ?? []
      into.push(w.id)
      fanIn.set(w.to_agent_id, into)
    }
    const wireEdges: Edge[] = !layers.delegation
      ? []
      : wires.map((w) => ({
          id: wireEdge(w.id),
          source: agentNode(w.from_agent_id),
          target: agentNode(w.to_agent_id),
          type: 'wire',
          animated: true,
          // The label is drawn by the custom edge, which needs to know where
          // this wire sits among the ones leaving the same agent. A label at
          // the midpoint is fine until wires converge — four authors each
          // wired to two critics put eight labels in the same place, and the
          // row reads "s submits decide submits". See WireEdge.
          data: {
            label: w.label || '',
            fanIndex: fanOut.get(w.from_agent_id)?.indexOf(w.id) ?? 0,
            fanCount: fanOut.get(w.from_agent_id)?.length ?? 1,
            fanInIndex: fanIn.get(w.to_agent_id)?.indexOf(w.id) ?? 0,
            fanInCount: fanIn.get(w.to_agent_id)?.length ?? 1,
          },
        }))
    // Only agent-scoped bindings become edges; a workspace-wide gear would
    // otherwise draw an edge to every agent and drown the graph.
    const bindingEdges: Edge[] = !layers.tools
      ? []
      : bindings
          .filter((b) => b.agent_id !== null)
          .map((b) => ({
            id: bindingEdge(b.id),
            source: gearNode(b.gear_id),
            target: agentNode(b.agent_id as number),
            className: 'bp-binding-edge',
          }))
    const memoryEdges: Edge[] = !layers.memory
      ? []
      : (graph?.edges ?? [])
          .filter((e) => e.kind === 'knows')
          .map((e, i) => ({
            id: `k-${e.from}-${e.to}-${i}`,
            source: e.from,
            target: e.to,
            className: 'bp-memory-edge',
            // Memory reaches an agent by binding, not by dragging a wire, so
            // these edges are shown but not deletable from the canvas.
            deletable: false,
          }))
    const egressEdges: Edge[] = !layers.outward || !egress?.enabled
      ? []
      : (egress?.grants ?? []).map((g) => ({
          id: egressEdge(g.id),
          source: OUTWARD,
          target: agentNode(g.agent_id),
          className: g.stale ? 'bp-egress-edge stale' : 'bp-egress-edge',
          label: g.stale ? 'lapsed — review it' : undefined,
        }))
    setEdges([...wireEdges, ...bindingEdges, ...memoryEdges, ...egressEdges])
  }, [wires, bindings, graph, layers, egress, setEdges])

  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return
      const from = nodes.find((n) => n.id === c.source)
      const to = nodes.find((n) => n.id === c.target)
      if (!from || !to) return

      // Dragging the internet node onto an agent IS the grant. Only a human
      // can do this: no agent tool creates, edits or deletes this edge.
      if (from.id === OUTWARD || to.id === OUTWARD) {
        if (from.id !== OUTWARD || to.data.kind !== 'agent' || !to.data.agent) {
          onError('Drag from the internet node to an agent to let that agent ask to search the web.')
          return
        }
        const target = to.data.agent
        const indirect = egress?.reach?.[target.name] ?? []
        const extra = indirect.length
          ? `\n\n${indirect.length} other agent${indirect.length === 1 ? '' : 's'} (${indirect.join(', ')}) can delegate to it, so they gain an indirect path outward.`
          : ''
        if (
          !confirm(
            `Let "${target.name}" ask to search the web?\n\nThis grants only the right to ASK: every search still stops and waits for you to approve that exact query.${extra}`,
          )
        )
          return
        api.egress
          .grant(wsId, target.id)
          .then(reloadGraph)
          .catch((e: Error) => onError(e.message))
        return
      }
      if (from.data.kind === 'memory' || to.data.kind === 'memory') {
        onError('Memory is attached in the Context tab, not by dragging: bind a document to the workspace or to one agent.')
        return
      }
      if (to.data.kind !== 'agent') {
        onError('Connections must end at an agent: wires grant delegation, gear links grant a tool.')
        return
      }
      if (from.data.kind === 'gear') {
        api.gears
          .bind(wsId, idOf(from.id), idOf(to.id))
          .then(reloadGraph)
          .catch((e: Error) => onError(e.message))
        return
      }
      api.wires
        .create(wsId, idOf(from.id), idOf(to.id))
        .then(() => {
          onChanged()
          return reloadGraph()
        })
        .catch((e: Error) => onError(e.message))
    },
    [nodes, wsId, reloadGraph, onChanged, onError],
  )

  const onEdgesDelete = useCallback(
    (deleted: Edge[]) => {
      Promise.all(
        deleted
          .filter((e) => e.id.startsWith('b-') || e.id.startsWith('w-') || e.id.startsWith('x-'))
          .map((e) => {
            if (e.id.startsWith('x-')) return api.egress.revoke(idOf(e.id))
            if (e.id.startsWith('b-')) return api.gears.unbind(idOf(e.id))
            return api.wires.remove(idOf(e.id))
          }),
      )
        .then(() => {
          onChanged()
          return reloadGraph()
        })
        .catch((err: Error) => onError(err.message))
    },
    [onChanged, reloadGraph, onError],
  )

  // Agent positions persist; gear nodes are laid out deterministically and
  // deliberately not stored.
  const handleNodesChange = useCallback(
    (changes: NodeChange<Node<NodeData>>[]) => {
      onNodesChange(changes)
      changes.forEach((ch) => {
        if (ch.type === 'position' && ch.dragging === false && ch.position && ch.id.startsWith('a-')) {
          api.agents
            .update(idOf(ch.id), { pos_x: ch.position.x, pos_y: ch.position.y })
            .catch((e: Error) => onError(e.message))
        }
      })
    },
    [onNodesChange, onError],
  )

  // Tidy re-lays out every agent and stores it, which is the difference
  // between this and a view mode: after tidying, the arrangement is what the
  // canvas will show next time, on every screen, to everybody.
  const tidy = useCallback(() => {
    const want = layered(agents, wires)
    Promise.all(
      agents.map((a) => {
        const p = want.get(a.id)
        if (!p || (a.pos_x === p.x && a.pos_y === p.y)) return Promise.resolve()
        return api.agents.update(a.id, { pos_x: p.x, pos_y: p.y })
      }),
    )
      .then(onChanged)
      .catch((e: Error) => onError(e.message))
  }, [agents, wires, onChanged, onError])

  // Where every node is, as one string. The fit has to react to POSITIONS, not
  // just to how many nodes there are — see below.
  const shape = nodes.map((n) => `${n.id}@${Math.round(n.position.x)},${Math.round(n.position.y)}`).join('|')

  /**
   * Fit the view until the operator takes it.
   *
   * THREE separate reasons the `fitView` prop was not enough, and each one on
   * its own left agents off the bottom of the canvas with no scrollbar to find
   * them with, because a pane you pan is not a page you scroll.
   *
   *   It fits at mount, and at mount this canvas is empty — the deck mounts
   *   every view at once and the graph arrives a request later.
   *
   *   fitView measures. It computes its bounding box from the RENDERED size of
   *   each node, so calling it on the tick the nodes arrive, before React Flow
   *   has laid any of them out, does nothing at all, in silence. Hence the
   *   wait for `measured`.
   *
   *   The layout moves AFTER the first fit. Agents land at a fallback position
   *   until the wires arrive, and then positionsFor spreads them by who
   *   delegates to whom — so a fit that ran once, on the first arrangement,
   *   was framing an arrangement that no longer existed a moment later.
   *
   * So it refits whenever the arrangement changes, and stops the first time
   * the operator pans, zooms or drags a node: after that the view is theirs.
   */
  /**
   * A drawer opening changes the size of the canvas, and the graph goes with
   * it.
   *
   * React Flow keeps its viewport transform across a resize, so nodes stay at
   * the same canvas coordinates while the window they are seen through gets
   * narrower — which put the whole graph off screen the moment a drawer came
   * out over the blueprint. Caught in a screenshot: the canvas empty, with the
   * dotted ground and nothing on it.
   *
   * Same rule as the fit below: not once the operator has taken the view. If
   * they panned somewhere deliberately, a drawer opening must not throw that
   * away.
   */
  useEffect(() => {
    const el = flowBox.current
    if (!el || typeof ResizeObserver === 'undefined') return
    let t = 0
    const ro = new ResizeObserver(() => {
      if (owned.current) return
      // Debounced: a drawer's transition fires this every frame, and fitting
      // on each one animates against the animation.
      window.clearTimeout(t)
      t = window.setTimeout(() => flow.current?.fitView({ padding: 0.18 }), 160)
    })
    ro.observe(el)
    return () => {
      window.clearTimeout(t)
      ro.disconnect()
    }
  }, [])

  useEffect(() => {
    if (owned.current || nodes.length === 0 || shape === lastFit.current) return
    let raf = 0
    let tries = 0
    const attempt = () => {
      const i = flow.current
      const live = i?.getNodes() ?? []
      if (i && live.length === nodes.length && live.every((n) => n.measured?.width)) {
        lastFit.current = shape
        i.fitView({ padding: 0.18 })
        return
      }
      // A second of frames, then give up rather than spin forever: a node that
      // never measures means something else is wrong, and an endless loop
      // would hide it.
      if (tries++ < 60) raf = requestAnimationFrame(attempt)
    }
    raf = requestAnimationFrame(attempt)
    return () => cancelAnimationFrame(raf)
  }, [shape, nodes.length])

  // ── Dropping a gear or an instruction on the canvas ────────────────────────
  //
  // WHERE IT LANDS IS THE SENTENCE. On an agent: that agent gets it. On empty
  // canvas: the whole workspace gets it — which is a real target, not the
  // absence of one, and is exactly what the "+ gear" control above does. That
  // control stays: a drag is not reachable from a keyboard.

  const agentUnder = useCallback(
    (x: number, y: number): Agent | null => {
      // The pointer position, not the drag image: elementFromPoint is the only
      // thing that knows what is under the cursor mid-drag, because React Flow
      // nodes are absolutely positioned inside a transformed pane and their
      // screen rectangles are not their layout rectangles.
      const node = document.elementFromPoint(x, y)?.closest('.react-flow__node')
      const id = node?.getAttribute('data-id')
      if (!id || !id.startsWith('a-')) return null
      return agents.find((a) => a.id === idOf(id)) ?? null
    },
    [agents],
  )

  const onCanvasDragOver = useCallback(
    (e: DragEvent) => {
      const kind = draggedKind(e)
      // Anything else — a file from the desktop, a link from another tab — is
      // refused by NOT preventing default, which is how the platform spells
      // refusal.
      if (!kind) return
      e.preventDefault()
      e.dataTransfer.dropEffect = 'copy'
      const over = agentUnder(e.clientX, e.clientY)?.id ?? null
      setLanded(null)
      // Same state, same object: this fires continuously while the pointer
      // moves, and a new object every frame re-renders every node.
      setDrag((p) => (p && p.kind === kind && p.over === over ? p : { kind, over }))
    },
    [agentUnder],
  )

  const onCanvasDrop = useCallback(
    (e: DragEvent) => {
      setDrag(null)
      const d = readDragged(e)
      if (!d) return
      e.preventDefault()
      const target = agentUnder(e.clientX, e.clientY)
      const where = target ? target.name : 'every agent here'

      if (d.kind === 'gear') {
        if (bindings.some((b) => b.gear_id === d.id && b.agent_id === (target?.id ?? null))) {
          setLanded({ text: `${where} already has ${d.name}.` })
          return
        }
        api.gears
          .bind(wsId, d.id, target?.id ?? null)
          .then(() => {
            // Landing something on a layer that is switched off would be a
            // drop with no visible result.
            setLayers((p) => (p.tools ? p : { ...p, tools: true }))
            return reloadGraph()
          })
          .then(() =>
            setLanded({
              text: `${d.name} → ${where}.`,
              // Binding an unapproved gear is allowed, and says nothing about
              // whether it may run — ForAgent only ever hands out approved
              // ones. So the link is drawn and the truth is stated, rather
              // than the drop being refused for a rule it does not enforce.
              warn:
                d.status === 'approved'
                  ? undefined
                  : `Nobody has approved ${d.name}. The link is drawn and it does nothing — no agent can call it until someone reads the source and says yes.`,
              gearId: d.status === 'approved' ? undefined : d.id,
            }),
          )
          .catch((err: Error) => onError(err.message))
        return
      }

      if (context.some((c) => c.path === d.path && c.agent_id === (target?.id ?? null))) {
        setLanded({ text: `${where} already reads ${d.name}.` })
        return
      }
      api.context
        .bind(wsId, d.path, target?.id ?? null)
        .then(() => {
          setLayers((p) => (p.memory ? p : { ...p, memory: true }))
          return reloadGraph()
        })
        .then(() => setLanded({ text: `${d.name} → ${where}.` }))
        .catch((err: Error) => onError(err.message))
    },
    [agentUnder, bindings, context, wsId, reloadGraph, onError],
  )

  // A plain confirmation goes on its own; one carrying a decision waits to be
  // dismissed, because a button that disappears while you reach for it is
  // worse than no button.
  useEffect(() => {
    if (!landed || landed.warn) return
    const t = setTimeout(() => setLanded(null), 4000)
    return () => clearTimeout(t)
  }, [landed])

  const inWorkspace = new Set(bindings.map((b) => b.gear_id))
  const addable = catalog.filter((g) => !inWorkspace.has(g.id))

  return (
    <div className="blueprint">
      {/* THE CONTROLS FLOAT ON THE WORK, they are not a strip above it.
          They used to stack two rows deep across the top of the cavity, with
          the canvas as a bordered box beneath — a toolbar and a document,
          which is a web page. Everywhere else in this product a control is a
          capsule standing on something; here it stands on the canvas, in the
          corner, and the canvas is the whole stage. */}
      <div className="bp-tools">
      {showHelp && (
        <p className="hint bp-help">
          Drag between nodes to connect — a wire IS the capability, not a picture of one. Select a link and press
          Delete to revoke it; double-click an agent to open it.
          <button className="linkish" onClick={() => { setShowHelp(false); localStorage.setItem('cogitorium.bpHelp', 'off') }}>
            got it
          </button>
        </p>
      )}
      <div className="row legend">
        {(['delegation', 'tools', 'memory', 'outward'] as const).map((layer) => (
          <button
            key={layer}
            className={`legend-item round ${layers[layer] ? '' : 'off'}`}
            onClick={() => setLayers((p) => ({ ...p, [layer]: !p[layer] }))}
            title={LAYER_HINT[layer]}
          >
            <span className={`legend-dot layer-${layer}`} />
            {layer}
          </button>
        ))}
        {layers.memory && (
          <span className="hint inline">
            🧠 memory branches · 📄 bound documents · 📘 instructions — dotted links show what each agent knows
          </span>
        )}
        {/* Tidy re-lays out every agent by its wires and STORES the result, so
            the arrangement is what everybody sees next time rather than a view
            this one screen holds. Dragging is still the last word: it writes a
            position too, and a stored one always wins. */}
        <button
          className="legend-item bp-tidy round"
          onClick={tidy}
          title="Arrange every agent by the wires between them, and keep it. Drag any of them afterwards."
        >
          ⤢ tidy
        </button>
      </div>
      {/* Adding things to the graph.
          
          There was no way to do it here at all. The only control was the gear
          dropdown below, which reads as a filter rather than an action, and an
          agent could not be created from the interface at any point — the API
          method existed and no component had ever called it. On a canvas whose
          whole subject is the graph, "add a node" is the one verb that has to
          be visible. */}
      <div className="row bp-add">
        <button className="bp-act round" onClick={() => setAdding((v) => !v)} title="Put a new agent on this canvas">
          {adding ? 'cancel' : '+ agent'}
        </button>
        {/* An ACTION, not a value.
            
            This was a <Select> — the control for holding a value you change —
            carrying the whole sentence "+ gear — add one to this workspace
            (all agents)…" as its placeholder. Measured: 391px wide against a
            78px button beside it, looking exactly like a text field, showing a
            sentence it never stopped showing because there was never a value
            to show instead. It read as a filter. The sentence is the button's
            title now, which is where a sentence goes. */}
        <DropMenu
          className="bp-act round"
          label="+ gear"
          title="Add a forged gear to this workspace — every agent in it can then call it"
          heading="add to every agent here"
          empty="Every forged gear is already in this workspace."
          items={addable.map((g) => ({ value: String(g.id), label: g.name, sub: g.status }))}
          onPick={(v) =>
            api.gears
              .bind(wsId, Number(v), null)
              .then(reloadGraph)
              .catch((err: Error) => onError(err.message))
          }
        />
      </div>

      {adding && (
        <NewAgentForm
          wsId={wsId}
          models={models}
          taken={new Set(agents.map((a) => a.name))}
          onDone={() => {
            setAdding(false)
            onChanged()
            void reloadGraph()
          }}
          onError={onError}
        />
      )}
      </div>
      <div
        ref={flowBox}
        className={`canvas ${drag ? 'bp-catching' : ''}`}
        onDragOver={onCanvasDragOver}
        onDragLeave={(e) => {
          // dragleave fires for every child crossed on the way in, so the only
          // one that means "gone" is the one whose destination is outside.
          if (!e.currentTarget.contains(e.relatedTarget as globalThis.Node | null)) setDrag(null)
        }}
        onDrop={onCanvasDrop}
      >
        {drag && (
          <p className="bp-catch-hint">
            {drag.over !== null
              ? `give it to ${agents.find((a) => a.id === drag.over)?.name ?? 'this agent'}`
              : drag.kind === 'gear'
                ? 'drop on an agent to give it there — or here, for every agent in this workspace'
                : 'drop on an agent for that agent alone — or here, for all of them'}
          </p>
        )}
        {landed && (
          <div className={`bp-landed ${landed.warn ? 'warned' : ''}`}>
            <strong>{landed.text}</strong>
            {landed.warn && <span>{landed.warn}</span>}
            <span className="row bp-landed-acts">
              {landed.gearId !== undefined && (
                <button className="primary" onClick={() => onReviewGear(landed.gearId as number)}>
                  review &amp; approve
                </button>
              )}
              <button onClick={() => setLanded(null)}>dismiss</button>
            </span>
          </div>
        )}
        <ReactFlow
          nodes={nodes.map((n) => ({
            ...n,
            className: `${n.className ?? ''}${drag && drag.over !== null && n.id === agentNode(drag.over) ? ' bp-catch' : ''}`,
            data: { ...n.data, label: nodeLabel(n.data) },
          }))}
          edges={edges}
          onNodesChange={handleNodesChange}
          onEdgesChange={onEdgesChange}
          onEdgesDelete={onEdgesDelete}
          onConnect={onConnect}
          onNodeDoubleClick={(_, n) => {
            const d = n.data as NodeData
            if (d.kind === 'agent' && d.agent) onSelectAgent(d.agent)
          }}
          edgeTypes={edgeTypes}
          onInit={(i) => {
            flow.current = i
          }}
          // A programmatic move passes a null event — that is the documented
          // way to tell our own fitView apart from the operator's hand.
          onMoveStart={(e) => {
            if (e) owned.current = true
          }}
          onNodeDragStart={() => {
            owned.current = true
          }}
          fitView
          proOptions={{ hideAttribution: true }}
        >
          <Background />
          <Controls />
        </ReactFlow>
      </div>
    </div>
  )
}

function nodeLabel(data: NodeData) {
  if (data.kind === 'outward') {
    return (
      <div className="bp-label">
        <strong>🌐 {data.destination || 'the internet'}</strong>
        <span className="muted">search only · https · one query at a time</span>
        <span className={data.egressOn ? 'bp-state' : 'warn'}>
          {data.egressOn ? 'drag onto an agent to let it ask' : 'off in this server’s configuration'}
        </span>
      </div>
    )
  }
  if (data.kind === 'memory') {
    const m = data.memory
    return (
      <div className="bp-label">
        <strong>
          {m?.kind === 'instruction' ? '📘' : m?.kind === 'document' ? '📄' : '🧠'} {m?.label}
        </strong>
        <span className="muted">{m?.detail}</span>
        <span className="bp-state">{KINDS[m?.kind ?? '']?.label ?? m?.kind}</span>
      </div>
    )
  }
  if (data.kind === 'gear') {
    const g = data.gear
    return (
      <div className="bp-label">
        <strong>⚙ {g?.name ?? 'gear'}</strong>
        <span className="muted">{g?.status ?? 'unknown'}</span>
        {data.workspaceWide && <span className="bp-state">all agents</span>}
      </div>
    )
  }
  const { agent, state } = data
  return (
    <div className="bp-label">
      <strong>
        {agent?.name}
        {agent?.is_orchestrator ? ' ★' : ''}
      </strong>
      <span className="muted">{agent?.model_label || 'no model'}</span>
      {state !== 'idle' && <span className="bp-state">{state}</span>}
    </div>
  )
}


/**
 * A new agent, put on the canvas by hand.
 *
 * Until now the only way to get one was to ask the orchestrator, or to call the
 * API yourself — api.agents.createAgent had been in the client the whole time
 * with no caller. For a product whose thesis is that the graph IS the program,
 * a canvas you cannot add a node to is a canvas you can only rearrange.
 *
 * A model is required and not defaulted. An agent without one cannot think, and
 * silently picking the first in the catalog would spend somebody's money on a
 * choice they did not make.
 */
function NewAgentForm({
  wsId,
  models,
  taken,
  onDone,
  onError,
}: {
  wsId: number
  models: Model[]
  taken: Set<string>
  onDone: () => void
  onError: (m: string) => void
}) {
  const [name, setName] = useState('')
  const [modelId, setModelId] = useState<number | ''>('')
  const [role, setRole] = useState('')
  const [busy, setBusy] = useState(false)

  const clash = taken.has(name.trim())

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (modelId === '') {
      onError('Give the agent a model: an agent with nothing to think with cannot take a turn.')
      return
    }
    setBusy(true)
    api.workspaces
      .createAgent(wsId, { name: name.trim(), role: role.trim(), model_id: modelId })
      .then(onDone)
      .catch((err: Error) => onError(err.message))
      .finally(() => setBusy(false))
  }

  return (
    <form className="card bp-new-agent" onSubmit={submit}>
      <div className="row">
        <input
          required
          autoFocus
          aria-label="Agent name"
          placeholder="reviewer"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Select
          value={modelId === '' ? '' : String(modelId)}
          aria-label="Model"
          placeholder="which model does it think with…"
          onChange={(v) => setModelId(v ? Number(v) : '')}
          options={models.map((m) => ({
            value: String(m.id),
            // label is optional in the catalog, so it alone renders four blank
            // rows on an install that never set one.
            label: `${m.provider_name} / ${m.label || m.model_name}`,
          }))}
        />
      </div>
      {clash && <span className="hint danger">this workspace already has an agent called “{name.trim()}”</span>}
      <label className="field">
        <span className="muted">role — the system prompt it always carries</span>
        <textarea
          rows={3}
          value={role}
          onChange={(e) => setRole(e.target.value)}
          placeholder="You review code for correctness. Say what is wrong and why, and nothing else."
        />
      </label>
      <div className="row spread">
        <span className="hint">
          It arrives unwired: nothing may delegate to it and it may delegate to nothing until you draw an edge. That is
          the point of the canvas — a new agent is a node with no capabilities, not a member of the team.
        </span>
        <button className="primary" type="submit" disabled={busy || !name.trim() || clash || modelId === ''}>
          add to the canvas
        </button>
      </div>
    </form>
  )
}
