import { DEFAULTS, type Layout, type PanelId, type SlotId } from './types'

// Ready-made arrangements.
//
// These are not decoration: an empty bench asks the operator to invent a
// workspace layout before they have used the product once. Each preset is one
// answer to "what am I doing right now", and the names say the task rather
// than the geometry — nobody thinks "I want a left dock and a bottom drawer".

export type Preset = {
  id: string
  name: string
  why: string
  build: () => Layout
}

function dock(panels: PanelId[], size: number, mode: 'push' | 'overlay' = 'push') {
  return { panels, active: panels[0] ?? null, size, open: true, mode }
}

function layout(slots: Partial<Record<SlotId, ReturnType<typeof dock>>>): Layout {
  const empty = (s: SlotId) => ({ ...DEFAULTS[s], panels: [], active: null })
  return {
    v: 1,
    slots: {
      left: slots.left ?? empty('left'),
      top: slots.top ?? empty('top'),
      main: slots.main ?? empty('main'),
      aux: slots.aux ?? empty('aux'),
      bottom: slots.bottom ?? empty('bottom'),
      right: slots.right ?? empty('right'),
    },
    maximized: null,
    floats: {},
  }
}

export const PRESETS: Preset[] = [
  {
    id: 'converse',
    name: 'Converse',
    why: 'Just the conversation and who is in it.',
    build: () => layout({ main: dock(['chat'], 0), right: dock(['agents'], 240) }),
  },
  {
    id: 'build',
    name: 'Build',
    why: 'Files and an editor beside the conversation, with a shell underneath.',
    build: () =>
      layout({
        left: dock(['files'], 280),
        main: dock(['chat'], 0),
        bottom: dock(['terminal'], 240),
        right: dock(['agents'], 220),
      }),
  },
  {
    id: 'wire',
    name: 'Wire up',
    why: 'The blueprint beside the chat — arrange the topology while the orchestrator talks.',
    build: () =>
      layout({ main: dock(['chat'], 0), aux: dock(['blueprint'], 520), right: dock(['agents'], 220) }),
  },
  {
    id: 'canvasfirst',
    name: 'Canvas-first',
    why: 'The graph takes the room; the conversation sits beside it, narrow.',
    build: () =>
      layout({ main: dock(['blueprint'], 0), aux: dock(['chat'], 380), right: dock(['agents'], 210) }),
  },
  {
    id: 'watch',
    name: 'Watch one agent',
    why: 'One agent under the microscope: its role, memory and spend next to the transcript.',
    build: () =>
      layout({ main: dock(['chat'], 0), aux: dock(['agent'], 460), right: dock(['agents'], 220) }),
  },
]

// A saved arrangement is a whole Layout under a name. Saving the geometry
// rather than a recipe is deliberate: the operator saves what they are looking
// at, including the widths they dragged.
export type SavedLayout = { id: string; name: string; layout: Layout }

const SAVED_KEY = 'cogitorium.layouts'

export function loadSaved(): SavedLayout[] {
  try {
    const raw = localStorage.getItem(SAVED_KEY)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter((s) => s && typeof s.name === 'string' && s.layout) : []
  } catch {
    return []
  }
}

export function storeSaved(all: SavedLayout[]) {
  try {
    localStorage.setItem(SAVED_KEY, JSON.stringify(all.slice(0, 20)))
  } catch {
    /* quota or private mode: saving simply does not persist this session */
  }
}
