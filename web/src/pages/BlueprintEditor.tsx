import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  addEdge,
  useEdgesState,
  useNodesState,
  type Connection,
  type Edge,
  type Node,
  type NodeChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { api, type Agent, type AgentStatus, type Wire } from '../api'

type AgentNodeData = { agent: Agent; state: string }

// Agents with no stored position get an org-chart layout: the orchestrator
// on top, workers in a row beneath it. Readable before anyone drags a node,
// and it matches how delegation actually flows.
function layout(agents: Agent[]): Map<number, { x: number; y: number }> {
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
  const [nodes, setNodes, onNodesChange] = useNodesState<Node<AgentNodeData>>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])

  // Wires resync whenever the agent set changes — the orchestrator may
  // have wired agents mid-turn.
  useEffect(() => {
    api.wires.list(wsId).then(setWires).catch((e: Error) => onError(e.message))
  }, [wsId, agents, onError])

  // Nodes are rebuilt only when the agent set changes. Status updates are
  // applied in place below: a rebuild mid-drag would snap the node back
  // and wipe the selection during live turns.
  const positions = useMemo(() => layout(agents), [agents])
  useEffect(() => {
    setNodes(
      agents.map((a) => ({
        id: String(a.id),
        position: positions.get(a.id) ?? { x: 0, y: 0 },
        data: { agent: a, state: 'idle' },
        type: 'default',
        className: `bp-node ${a.is_orchestrator ? 'bp-orchestrator' : ''} bp-idle`,
      })),
    )
  }, [agents, positions, setNodes])

  useEffect(() => {
    setNodes((prev) =>
      prev.map((n) => {
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
    setEdges(
      wires.map((w) => ({
        id: String(w.id),
        source: String(w.from_agent_id),
        target: String(w.to_agent_id),
        label: w.label || undefined,
        animated: true,
      })),
    )
  }, [wires, setEdges])

  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return
      api.wires
        .create(wsId, Number(c.source), Number(c.target))
        .then((w) => {
          setEdges((eds) => addEdge({ ...c, id: String(w.id), animated: true }, eds))
          onChanged()
        })
        .catch((e: Error) => onError(e.message))
    },
    [wsId, setEdges, onChanged, onError],
  )

  const onEdgesDelete = useCallback(
    (deleted: Edge[]) => {
      deleted.forEach((e) =>
        api.wires
          .remove(Number(e.id))
          .then(onChanged)
          .catch((err: Error) => onError(err.message)),
      )
    },
    [onChanged, onError],
  )

  // Persist a node position once the drag ends, not on every frame.
  const handleNodesChange = useCallback(
    (changes: NodeChange<Node<AgentNodeData>>[]) => {
      onNodesChange(changes)
      changes.forEach((ch) => {
        if (ch.type === 'position' && ch.dragging === false && ch.position) {
          api.agents
            .update(Number(ch.id), { pos_x: ch.position.x, pos_y: ch.position.y })
            .catch((e: Error) => onError(e.message))
        }
      })
    },
    [onNodesChange, onError],
  )

  return (
    <div className="blueprint">
      <p className="hint">
        Wires are the delegation capability, not decoration: an agent may delegate only along its outgoing edges.
        Drag from a node's edge to connect; select a wire and press Delete to revoke it. Double-click a node to open
        the agent.
      </p>
      <div className="canvas">
        <ReactFlow
          nodes={nodes.map((n) => ({ ...n, data: { ...n.data, label: nodeLabel(n.data) } }))}
          edges={edges}
          onNodesChange={handleNodesChange}
          onEdgesChange={onEdgesChange}
          onEdgesDelete={onEdgesDelete}
          onConnect={onConnect}
          onNodeDoubleClick={(_, n) => onSelectAgent((n.data as AgentNodeData).agent)}
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

function nodeLabel(data: AgentNodeData) {
  const { agent, state } = data
  return (
    <div className="bp-label">
      <strong>
        {agent.name}
        {agent.is_orchestrator ? ' ★' : ''}
      </strong>
      <span className="muted">{agent.model_label || 'no model'}</span>
      {state !== 'idle' && <span className="bp-state">{state}</span>}
    </div>
  )
}
