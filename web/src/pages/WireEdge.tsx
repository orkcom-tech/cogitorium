import { memo, useMemo } from 'react'
import {
  BaseEdge,
  EdgeLabelRenderer,
  getSmoothStepPath,
  useNodes,
  type EdgeProps,
  type Node,
} from '@xyflow/react'

// A delegation wire, drawn with its label somewhere other than the middle.
//
// React Flow's own label sits at the midpoint, which is right for one edge and
// wrong for a graph: the midpoints of wires that converge are in the same
// place. An orchestrator handing work to four authors who each submit to two
// critics puts eight labels on top of one another, and what should read
// "submits" reads "s submits decide submits" — the picture that is supposed to
// show the arrangement is where the arrangement becomes unreadable.
//
// There are two different crowds and one axis will not clear both. Wires
// LEAVING one agent all start at the same handle, so their labels are spread
// along the curve — by the time each is a third of the way out, the curves have
// fanned apart. Wires ARRIVING at one agent converge instead, and pushing those
// labels further along only drives them together; they are offset sideways
// instead, across the bundle rather than along it. Tuning one axis to fix both
// is whack-a-mole, which is how this was first written and why it isn't now:
// widening the spread separated four "writes" and immediately stacked two
// "submits" at the critic they both fed.

// Where the first label sits, and how much further along each next sibling
// goes. The window is wide on purpose: wires leaving one agent all start at the
// same handle and have barely separated near it, so labels bunched close to the
// source still touch even when their parameters differ. They are pushed out to
// where the curves have actually fanned apart. Four out of one agent is
// comfortable and six is the practical limit; past that they crowd again, which
// is a fair signal that the graph itself has too much going on.
const firstStop = 0.28
const stopGap = 0.18
const lastStop = 0.84

// How far apart the labels of wires converging on one agent sit, measured
// across the bundle. Roughly a line's height: enough to clear, small enough
// that a label still plainly belongs to its own curve.
const acrossGap = 26

function stopFor(index: number, count: number): number {
  if (count <= 1) return 0.5
  const span = Math.min(firstStop + stopGap * (count - 1), lastStop) - firstStop
  return firstStop + (span * index) / (count - 1)
}

// pointOn evaluates the path at t, where t is a fraction of its LENGTH.
//
// The wires used to be bezier curves and this used to read the four control
// points back out of "M x,y C …" and evaluate the cubic. They are orthogonal
// now — a run of straight segments joined by small corner arcs — so there is no
// cubic to evaluate, and a regex that fails silently would have dropped every
// label back onto the midpoint, which is the exact collision the spreading
// above exists to prevent.
//
// A polyline is measured rather than parameterised, and that is an improvement
// rather than a compromise: distance along the path is what the eye actually
// wants, and the old comment said as much while settling for the cubic
// parameter because measuring a curve properly needed the DOM. A polyline needs
// nothing but arithmetic.
//
// The corner arcs are treated as their control points. A corner is a handful of
// pixels across and a label is never placed on one, so the error is smaller
// than the rounding on the coordinates it came from.
const numbers = /-?\d+(?:\.\d+)?/g

function polyline(path: string): [number, number][] {
  const n = path.match(numbers)
  if (!n || n.length < 4) return []
  const pts: [number, number][] = []
  for (let i = 0; i + 1 < n.length; i += 2) {
    const p: [number, number] = [Number(n[i]), Number(n[i + 1])]
    // Consecutive duplicates are common where a segment has zero length, and
    // they would put a zero-length span in the walk below.
    const last = pts[pts.length - 1]
    if (!last || last[0] !== p[0] || last[1] !== p[1]) pts.push(p)
  }
  return pts
}

function pointOn(
  path: string,
  t: number,
  fallback: [number, number],
  across = 0,
): [number, number] {
  const pts = polyline(path)
  if (pts.length < 2) return fallback

  const seg: number[] = []
  let total = 0
  for (let i = 1; i < pts.length; i++) {
    const d = Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1])
    seg.push(d)
    total += d
  }
  if (total <= 0) return fallback

  let want = Math.max(0, Math.min(1, t)) * total
  for (let i = 0; i < seg.length; i++) {
    if (want > seg[i] && i < seg.length - 1) {
      want -= seg[i]
      continue
    }
    const u = seg[i] > 0 ? want / seg[i] : 0
    const [x0, y0] = pts[i]
    const [x1, y1] = pts[i + 1]
    const x = x0 + (x1 - x0) * u
    const y = y0 + (y1 - y0) * u
    if (!across) return [x, y]
    // Sideways means perpendicular to THIS segment: wires in a bundle arrive
    // on different axes, and an offset in a fixed direction would push labels
    // apart on one approach and along the same line on another.
    const dx = x1 - x0
    const dy = y1 - y0
    const len = Math.hypot(dx, dy) || 1
    return [x - (dy / len) * across, y + (dx / len) * across]
  }
  return fallback
}

// clear reports whether a point is far enough from every node's box.
//
// Spreading the labels apart fixed them colliding with each other and left the
// other collision untouched: a wire between two distant agents passes over the
// ones in between, and its label lands on somebody else's card. A label sitting
// on a node reads as though it belongs to that node, which is worse than
// crowding — it is wrong rather than merely dense.
//
// Nodes are rendered above the label layer, so this cannot be solved by
// stacking order. The margin is generous because a plate that merely touches a
// border still looks attached to it.
//
// The label's OWN size is part of the test. Checking its centre point against
// the node boxes is the obvious version and it does not work: the first attempt
// did exactly that, reported every stop clear, and "decides" still sat across a
// critic's card — the point was outside the box while sixty pixels of plate
// were inside it. The extent is estimated from the character count rather than
// measured, because the answer is needed while deciding where to put the
// element that would do the measuring.
const nodeMargin = 10
const charWidth = 3.6
const labelPadX = 8
const labelHalfHeight = 10

function clear(x: number, y: number, halfWidth: number, nodes: Node[]): boolean {
  return !nodes.some((n) => {
    const w = n.measured?.width ?? 0
    const h = n.measured?.height ?? 0
    if (!w || !h) return false
    return (
      x + halfWidth > n.position.x - nodeMargin &&
      x - halfWidth < n.position.x + w + nodeMargin &&
      y + labelHalfHeight > n.position.y - nodeMargin &&
      y - labelHalfHeight < n.position.y + h + nodeMargin
    )
  })
}

// stopClearOf walks outward from the wire's preferred stop until the label has
// room, and gives up on the preferred one if nothing along the curve is free —
// a wire that runs the length of a crowded canvas has nowhere good to put its
// label, and pretending otherwise would just move the problem.
function stopClearOf(
  path: string,
  base: number,
  across: number,
  fallback: [number, number],
  halfWidth: number,
  nodes: Node[],
) {
  for (let step = 0; step <= 8; step++) {
    for (const t of step === 0 ? [base] : [base + step * 0.05, base - step * 0.05]) {
      if (t < 0.1 || t > 0.9) continue
      const p = pointOn(path, t, fallback, across)
      if (clear(p[0], p[1], halfWidth, nodes)) return p
    }
  }
  return pointOn(path, base, fallback, across)
}

function WireEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  style,
  data,
}: EdgeProps) {
  // Orthogonal, with a corner radius.
  //
  // A curve says "these two are related"; a right-angled run says "this goes
  // there, by this route". In a graph where every wire is a capability
  // somebody granted, the second is the honest picture — and it is also the
  // readable one, because parallel runs share a lane instead of fanning into
  // a bundle of near-identical arcs.
  const [path, midX, midY] = getSmoothStepPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    borderRadius: 12,
  })

  const label = typeof data?.label === 'string' ? data.label : ''
  const index = typeof data?.fanIndex === 'number' ? data.fanIndex : 0
  const count = typeof data?.fanCount === 'number' ? data.fanCount : 1
  const inIndex = typeof data?.fanInIndex === 'number' ? data.fanInIndex : 0
  const inCount = typeof data?.fanInCount === 'number' ? data.fanInCount : 1
  const across = inCount > 1 ? (inIndex - (inCount - 1) / 2) * acrossGap : 0

  const nodes = useNodes()
  const [x, y] = useMemo(
    () =>
      stopClearOf(
        path,
        stopFor(index, count),
        across,
        [midX, midY],
        label.length * charWidth + labelPadX,
        nodes,
      ),
    [path, index, count, across, midX, midY, label, nodes],
  )

  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style} />
      {label && (
        <EdgeLabelRenderer>
          <div
            className="bp-wire-label nodrag nopan"
            style={{ transform: `translate(-50%, -50%) translate(${x}px, ${y}px)` }}
          >
            {label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

export default memo(WireEdge)
