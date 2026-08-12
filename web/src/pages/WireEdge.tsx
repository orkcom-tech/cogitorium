import { memo, useMemo } from 'react'
import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
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

// pointOn evaluates the cubic the path describes, at t.
//
// The path is measured rather than guessed: getBezierPath returns a single
// "M x,y C …" and reading the four control points back out of it means the
// label lands on the curve React Flow actually drew, whichever way round the
// handles ended up. Measuring it through the DOM instead — getPointAtLength on
// a detached path — would give distance along the curve rather than this
// parameter, which is closer to what the eye wants; it is also a browser API
// that has historically wanted the element to be in the document, and this runs
// while React is still rendering. Not worth it: for these curves the two agree
// to within a few pixels.
const cubic =
  /^M\s*(-?[\d.]+),\s*(-?[\d.]+)\s*C\s*(-?[\d.]+),\s*(-?[\d.]+)\s+(-?[\d.]+),\s*(-?[\d.]+)\s+(-?[\d.]+),\s*(-?[\d.]+)/

function pointOn(
  path: string,
  t: number,
  fallback: [number, number],
  across = 0,
): [number, number] {
  const m = cubic.exec(path)
  if (!m) return fallback
  const [x0, y0, x1, y1, x2, y2, x3, y3] = m.slice(1).map(Number)
  const u = 1 - t
  const x = u * u * u * x0 + 3 * u * u * t * x1 + 3 * u * t * t * x2 + t * t * t * x3
  const y = u * u * u * y0 + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t * t * t * y3
  if (!across) return [x, y]
  // Sideways means perpendicular to the curve here, not simply left: the
  // wires in a bundle arrive at every angle, and an offset in a fixed
  // direction would push labels apart on one approach and along the same line
  // on another.
  const dx = 3 * u * u * (x1 - x0) + 6 * u * t * (x2 - x1) + 3 * t * t * (x3 - x2)
  const dy = 3 * u * u * (y1 - y0) + 6 * u * t * (y2 - y1) + 3 * t * t * (y3 - y2)
  const len = Math.hypot(dx, dy) || 1
  return [x - (dy / len) * across, y + (dx / len) * across]
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
  const [path, midX, midY] = getBezierPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
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
