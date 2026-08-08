import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Connection,
  type Edge,
  type Node,
  type NodeChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { api, type Agent, type AgentStatus, type Gear, type GearBinding, type Wire } from '../api'

// Node and edge ids are namespaced because the canvas mixes two kinds of
// each: agents and gears, delegation wires and gear bindings.
const agentNode = (id: number) => `a-${id}`
const gearNode = (id: number) => `g-${id}`
const wireEdge = (id: number) => `w-${id}`
const bindingEdge = (id: number) => `b-${id}`
const idOf = (nodeOrEdgeId: string) => Number(nodeOrEdgeId.slice(2))

type NodeData = {
  kind: 'agent' | 'gear'
  agent?: Agent
  gear?: Gear
  workspaceWide?: boolean
  state: string
}

// Agents keep their stored positions; gears are laid out beneath them.
function layoutAgents(agents: Agent[]): Map<number, { x: number; y: number }> {
  const out = new Map<number, { x: number; y: number }>()
  agents.forEach((a) => {
    if (a.pos_x != null && a.pos_y != null) out.set(a.id, { x: a.pos_x, y: a.pos_y })
  })
  const unplaced = agents.filter((a) => !out.has(a.id))
  const workers = unplaced.filter((a) => !a.is_orchestrator)
  unplaced.forEach((a) => {
    if (a.is_orchestrator) {
      out.set(a.id, { x: 0, y: 0 })
      return
    }
    const idx = workers.indexOf(a)
    out.set(a.id, { x: (idx - (workers.length - 1) / 2) * 220, y: 220 })
  })
  return out
}

export default function BlueprintEditor({
  wsId,
  agents,
  statuses,
  onChanged,
  onSelectAgent,
  onError,
}: {
  wsId: number
  agents: Agent[]
  statuses: Map<number, AgentStatus>
  onChanged: () => void
  onSelectAgent: (a: Agent) => void
  onError: (msg: string) => void
}) {
  const [wires, setWires] = useState<Wire[]>([])
  const [bindings, setBindings] = useState<GearBinding[]>([])
  const [catalog, setCatalog] = useState<Gear[]>([])
  const [nodes, setNodes, onNodesChange] = useNodesState<Node<NodeData>>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])

  const reloadGraph = useCallback(
    () =>
      Promise.all([api.wires.list(wsId), api.gears.bindings(wsId), api.gears.list()])
        .then(([w, b, c]) => {
          setWires(w)
          setBindings(b)
          setCatalog(c)
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
  const positions = useMemo(() => layoutAgents(agents), [agents])

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
    const gearIds = [...new Set(bindings.map((b) => b.gear_id))]
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

    setNodes([...agentNodes, ...gearNodes])
  }, [agents, positions, bindings, gearById, setNodes])

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
    const wireEdges: Edge[] = wires.map((w) => ({
      id: wireEdge(w.id),
      source: agentNode(w.from_agent_id),
      target: agentNode(w.to_agent_id),
      label: w.label || undefined,
      animated: true,
    }))
    // Only agent-scoped bindings become edges; a workspace-wide gear would
    // otherwise draw an edge to every agent and drown the graph.
    const bindingEdges: Edge[] = bindings
      .filter((b) => b.agent_id !== null)
      .map((b) => ({
        id: bindingEdge(b.id),
        source: gearNode(b.gear_id),
        target: agentNode(b.agent_id as number),
        className: 'bp-binding-edge',
      }))
    setEdges([...wireEdges, ...bindingEdges])
  }, [wires, bindings, setEdges])

  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return
      const from = nodes.find((n) => n.id === c.source)
      const to = nodes.find((n) => n.id === c.target)
      if (!from || !to) return

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
        deleted.map((e) =>
          e.id.startsWith('b-') ? api.gears.unbind(idOf(e.id)) : api.wires.remove(idOf(e.id)),
        ),
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

  const inWorkspace = new Set(bindings.map((b) => b.gear_id))
  const addable = catalog.filter((g) => !inWorkspace.has(g.id))

  return (
    <div className="blueprint">
      <p className="hint">
        Wires are the delegation capability, not decoration: an agent may delegate only along its outgoing edges.
        A gear linked to an agent is a tool that agent may call. Drag between nodes to connect, select a link and
        press Delete to revoke it. Double-click an agent to open it.
      </p>
      <div className="row">
        <select
          className="grow"
          value=""
          onChange={(e) => {
            if (!e.target.value) return
            api.gears
              .bind(wsId, Number(e.target.value), null)
              .then(reloadGraph)
              .catch((err: Error) => onError(err.message))
          }}
        >
          <option value="">add a gear to this workspace (all agents)…</option>
          {addable.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name} ({g.status})
            </option>
          ))}
        </select>
      </div>
      <div className="canvas">
        <ReactFlow
          nodes={nodes.map((n) => ({ ...n, data: { ...n.data, label: nodeLabel(n.data) } }))}
          edges={edges}
          onNodesChange={handleNodesChange}
          onEdgesChange={onEdgesChange}
          onEdgesDelete={onEdgesDelete}
          onConnect={onConnect}
          onNodeDoubleClick={(_, n) => {
            const d = n.data as NodeData
            if (d.kind === 'agent' && d.agent) onSelectAgent(d.agent)
          }}
          fitView
          proOptions={{ hideAttribution: true }}
        >
          <Background />
          <Controls />
          <MiniMap pannable zoomable />
        </ReactFlow>
      </div>
    </div>
  )
}

function nodeLabel(data: NodeData) {
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
