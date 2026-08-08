import { useEffect, useMemo } from 'react'
import {
  Background,
  Controls,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Edge,
  type Node,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { GraphData, GraphNode } from '../api'

// Node kinds carry meaning, so each gets a shape and a colour of its own and
// the legend spells it out. A graph where everything looks alike is a
// picture of connectivity, not an explanation.
export const KINDS: Record<string, { label: string; className: string }> = {
  agent: { label: 'agent', className: 'gn-agent' },
  gear: { label: 'gear (tool)', className: 'gn-gear' },
  shared: { label: 'workspace memory', className: 'gn-shared' },
  private: { label: "agent's own memory", className: 'gn-private' },
  document: { label: 'bound document', className: 'gn-document' },
  instruction: { label: 'instruction', className: 'gn-instruction' },
  user: { label: 'user', className: 'gn-user' },
  team: { label: 'team', className: 'gn-team' },
  workspace: { label: 'workspace', className: 'gn-workspace' },
}

// Edge kinds are the relationships themselves, so they are drawn
// differently rather than labelled identically.
const EDGE_STYLE: Record<string, { className: string; animated: boolean }> = {
  delegates: { className: 'ge-delegates', animated: true },
  tool: { className: 'ge-tool', animated: false },
  knows: { className: 'ge-knows', animated: false },
  owns: { className: 'ge-owns', animated: false },
  shared: { className: 'ge-shared', animated: false },
  member: { className: 'ge-member', animated: false },
}

// Columns place a kind at a fixed depth, so the same sort of thing always
// appears in the same band and the eye learns the layout once.
const COLUMN: Record<string, number> = {
  user: 0,
  team: 1,
  workspace: 2,
  shared: 0,
  private: 0,
  document: 0,
  instruction: 0,
  agent: 1,
  gear: 2,
}

function layout(nodes: GraphNode[]): Map<string, { x: number; y: number }> {
  const byColumn = new Map<number, GraphNode[]>()
  nodes.forEach((n) => {
    const col = COLUMN[n.kind] ?? 1
    byColumn.set(col, [...(byColumn.get(col) ?? []), n])
  })
  const out = new Map<string, { x: number; y: number }>()
  byColumn.forEach((group, col) => {
    group.forEach((n, i) => {
      out.set(n.id, { x: col * 320, y: (i - (group.length - 1) / 2) * 110 })
    })
  })
  return out
}

export default function GraphCanvas({
  data,
  layers,
  onToggleLayer,
  onSelect,
  emptyHint,
}: {
  data: GraphData | null
  layers: Record<string, boolean>
  onToggleLayer: (kind: string) => void
  onSelect?: (node: GraphNode) => void
  emptyHint: string
}) {
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])

  const visible = useMemo(() => {
    if (!data) return null
    const keptNodes = data.nodes.filter((n) => layers[n.kind] !== false)
    const ids = new Set(keptNodes.map((n) => n.id))
    return {
      nodes: keptNodes,
      edges: data.edges.filter((e) => ids.has(e.from) && ids.has(e.to)),
    }
  }, [data, layers])

  useEffect(() => {
    if (!visible) return
    const pos = layout(visible.nodes)
    setNodes(
      visible.nodes.map((n) => ({
        id: n.id,
        position: pos.get(n.id) ?? { x: 0, y: 0 },
        data: {
          node: n,
          label: (
            <div className="gn-label">
              <strong>{n.label}</strong>
              {n.detail && <span className="muted">{n.detail}</span>}
              {n.status && <span className={`status ${n.status}`}>{n.status}</span>}
            </div>
          ),
        },
        className: `bp-node ${KINDS[n.kind]?.className ?? ''}`,
      })),
    )
    setEdges(
      visible.edges.map((e, i) => ({
        id: `${e.kind}-${e.from}-${e.to}-${i}`,
        source: e.from,
        target: e.to,
        label: e.label || undefined,
        animated: EDGE_STYLE[e.kind]?.animated ?? false,
        className: EDGE_STYLE[e.kind]?.className ?? '',
      })),
    )
  }, [visible, setNodes, setEdges])

  const presentKinds = useMemo(
    () => [...new Set((data?.nodes ?? []).map((n) => n.kind))],
    [data],
  )

  if (!data) return <p className="hint">drawing…</p>
  if (data.nodes.length === 0) return <p className="hint">{emptyHint}</p>

  return (
    <div className="blueprint">
      <div className="row legend">
        {presentKinds.map((kind) => (
          <button
            key={kind}
            className={`legend-item ${layers[kind] === false ? 'off' : ''}`}
            onClick={() => onToggleLayer(kind)}
            title="Show or hide this layer"
          >
            <span className={`legend-dot ${KINDS[kind]?.className ?? ''}`} />
            {KINDS[kind]?.label ?? kind}
          </button>
        ))}
      </div>
      <div className="canvas">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={(_, n) => onSelect?.((n.data as { node: GraphNode }).node)}
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
