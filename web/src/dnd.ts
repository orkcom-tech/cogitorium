// What can be picked up in a drawer and dropped on the blueprint.
//
// The drawers list things — gears, instructions — and the blueprint is where
// those things are given to an agent. Before this, giving one to an agent
// meant leaving the canvas, finding the agent in a panel, and choosing from a
// dropdown; the drawer that already had the gear on screen could do nothing
// with it. Dragging is the shortest true sentence for what is happening: this
// tool, that agent.
//
// The contract is a MIME type per kind, deliberately, rather than one type
// with the kind inside the payload. `dataTransfer.getData` returns an empty
// string during dragover — every browser withholds the payload until the drop,
// so that a page cannot read what is passing over it. `types` IS readable
// then. So the kind has to be in the type NAME, or the canvas cannot know what
// it is about to accept while the pointer is still moving, and cannot say so.

import type { DragEvent } from 'react'

export type Dragged =
  | { kind: 'gear'; id: number; name: string; status: string }
  | { kind: 'instruction'; id: number; name: string; path: string }

const TYPE = {
  gear: 'application/x-cogitorium-gear',
  instruction: 'application/x-cogitorium-instruction',
} as const

/** Handler for onDragStart on whatever is being offered. */
export function dragging(d: Dragged) {
  return (e: DragEvent) => {
    e.dataTransfer.setData(TYPE[d.kind], JSON.stringify(d))
    // text/plain as well: Firefox will not start a drag without it, and it is
    // what lands if the thing is dropped somewhere that is not our canvas.
    e.dataTransfer.setData('text/plain', d.name)
    e.dataTransfer.effectAllowed = 'copy'
    e.stopPropagation()
  }
}

/** What kind is passing over — all that is knowable before the drop. */
export function draggedKind(e: DragEvent): Dragged['kind'] | null {
  const types = Array.from(e.dataTransfer.types)
  if (types.includes(TYPE.gear)) return 'gear'
  if (types.includes(TYPE.instruction)) return 'instruction'
  return null
}

/**
 * The payload, on drop.
 *
 * Total: anything that is not one of our two types, or is not the shape we
 * wrote, comes back null. A drop is a place where content from outside the
 * page arrives — a file, a link, a selection from another tab — and none of it
 * may become an action here.
 */
export function readDragged(e: DragEvent): Dragged | null {
  for (const kind of ['gear', 'instruction'] as const) {
    const raw = e.dataTransfer.getData(TYPE[kind])
    if (!raw) continue
    try {
      const d = JSON.parse(raw) as Dragged
      if (d?.kind !== kind || typeof d.id !== 'number' || typeof d.name !== 'string') return null
      if (kind === 'instruction' && typeof (d as { path?: unknown }).path !== 'string') return null
      return d
    } catch {
      return null
    }
  }
  return null
}
