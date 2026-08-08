// The bench: a flat map of named slots rendered as CSS Grid areas.
//
// A panel's position is a STYLE, not a place in the DOM. That is the whole
// design in one sentence, and it is not an aesthetic preference: reparenting a
// panel remounts its subtree, and TerminalPage's cleanup runs ws.close() and
// term.dispose(), so moving a panel between parents would silently kill the
// operator's shell. Every panel is a direct child of .bench for its whole life
// and moves by having a different grid-area written onto it.

export type SlotId = 'main' | 'aux' | 'bottom' | 'right'

// Fixed rank so DOM order matches reading order for tab sequence and screen
// readers, without ever reordering the array itself.
export const SLOT_ORDER: SlotId[] = ['main', 'aux', 'bottom', 'right']

export type PanelId = string

export type Dock = {
  panels: PanelId[]
  active: PanelId | null
  /** px along this slot's axis */
  size: number
  /** false collapses the dock to a rail of tab labels; panels stay MOUNTED */
  open: boolean
}

export type Layout = {
  v: 1
  slots: Record<SlotId, Dock>
  maximized: PanelId | null
}

export const DEFAULTS: Record<SlotId, Dock> = {
  main: { panels: ['chat'], active: 'chat', size: 0, open: true },
  aux: { panels: [], active: null, size: 420, open: true },
  bottom: { panels: [], active: null, size: 260, open: true },
  right: { panels: ['agents'], active: 'agents', size: 240, open: true },
}

export const MIN_MAIN = 320
export const RAIL = 32

export function defaultLayout(): Layout {
  return {
    v: 1,
    slots: {
      main: { ...DEFAULTS.main, panels: [...DEFAULTS.main.panels] },
      aux: { ...DEFAULTS.aux, panels: [] },
      bottom: { ...DEFAULTS.bottom, panels: [] },
      right: { ...DEFAULTS.right, panels: [...DEFAULTS.right.panels] },
    },
    maximized: null,
  }
}

/**
 * parseLayout is total: it rejects rather than repairs, and falls back
 * field-by-field to the default rather than wiping the whole arrangement.
 *
 * A version bump that silently destroys the one thing the operator hand-tuned
 * is exactly the "why does my workbench look different today" failure, so the
 * merge is per field and `v` is kept only as a diagnostic.
 */
export function parseLayout(raw: unknown, known: (id: PanelId) => boolean): Layout {
  const out = defaultLayout()
  if (!raw || typeof raw !== 'object') return out
  const src = raw as Record<string, unknown>
  const slots = src.slots
  if (!slots || typeof slots !== 'object') return out

  // A panel id appearing in two slots is kept in the first, by fixed order.
  const claimed = new Set<PanelId>()

  for (const slot of SLOT_ORDER) {
    const d = (slots as Record<string, unknown>)[slot]
    if (!d || typeof d !== 'object') continue
    const dock = d as Record<string, unknown>

    let list = out.slots[slot].panels
    if (Array.isArray(dock.panels)) {
      list = []
      for (const p of dock.panels) {
        // Unknown kinds are dropped rather than repaired: a panel id nothing
        // can render is a permanent white screen if it reaches the tree.
        if (typeof p !== 'string' || !known(p) || claimed.has(p)) continue
        claimed.add(p)
        list.push(p)
      }
    } else {
      list.forEach((p) => claimed.add(p))
    }

    const size = typeof dock.size === 'number' && Number.isFinite(dock.size) && dock.size > 0
      ? dock.size
      : DEFAULTS[slot].size

    out.slots[slot] = {
      panels: list,
      active: typeof dock.active === 'string' && list.includes(dock.active) ? dock.active : (list[0] ?? null),
      size,
      open: dock.open === undefined ? true : dock.open === true,
    }
  }

  if (typeof src.maximized === 'string' && known(src.maximized)) out.maximized = src.maximized
  return out
}

/**
 * fit clamps a layout to the viewport at PAINT time, never on write.
 *
 * Stored sizes are the operator's intent and are kept raw — plug the monitor
 * back in and the real arrangement returns. The clamp is per AXIS rather than
 * per slot, because two independent `viewport * 0.6` clamps happily permit
 * 120% of an axis and collapse the centre to nothing.
 */
export function fit(layout: Layout, vw: number, vh: number): Layout {
  const s = layout.slots
  const wRight = s.right.panels.length === 0 ? 0 : s.right.open ? s.right.size : RAIL
  const wAux = s.aux.panels.length === 0 ? 0 : s.aux.open ? s.aux.size : RAIL
  const hBottom = s.bottom.panels.length === 0 ? 0 : s.bottom.open ? s.bottom.size : RAIL

  let right = wRight
  let aux = wAux
  const horizontal = right + aux
  const room = Math.max(0, vw - MIN_MAIN)
  if (horizontal > room && horizontal > 0) {
    const scale = room / horizontal
    right = Math.floor(right * scale)
    aux = Math.floor(aux * scale)
  }
  const bottom = Math.min(hBottom, Math.max(0, vh - 160))

  return {
    ...layout,
    slots: {
      ...s,
      right: { ...s.right, size: s.right.open ? right : s.right.size },
      aux: { ...s.aux, size: s.aux.open ? aux : s.aux.size },
      bottom: { ...s.bottom, size: s.bottom.open ? bottom : s.bottom.size },
    },
  }
}

/** px guards the render edge: an invalid var() makes the WHOLE declaration
 *  invalid, so grid-template-columns would fall back to `none`, every panel
 *  would stack in one column, and because it is persisted the reload would
 *  reproduce it exactly. */
export function px(n: number, fallback: number): string {
  return `${Number.isFinite(n) && n >= 0 ? n : fallback}px`
}
