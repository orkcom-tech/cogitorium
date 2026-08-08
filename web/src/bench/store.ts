import { useCallback, useEffect, useState } from 'react'
import { session } from '../session'
import { SLOT_ORDER, defaultLayout, parseLayout, type Layout, type PanelId, type SlotId } from './types'

// Layout persistence, split by lifetime.
//
// sessionStorage holds THIS tab's arrangement; localStorage holds the seed a
// new tab starts from. An agentic workbench's normal use is one tab watching a
// long run while you work in another, and a single shared key means the tab
// you were not using dictates the arrangement of the one you were. Splitting
// costs ten lines and removes the need for any reconciliation protocol.

const TAB_KEY = 'cogitorium.layout'

// The seed key carries server and user. Workspace ids are SQLite rowids, so
// every server has a workspace 1; without the server segment a work server
// would inherit the home server's arrangement. Without the user segment,
// signing out would hand the next operator on a shared machine the previous
// one's bench — and their panel titles.
function seedKey(userId: number): string {
  const server = session.server() || 'local'
  let hash = 0
  for (let i = 0; i < server.length; i++) hash = (hash * 31 + server.charCodeAt(i)) | 0
  return `cogitorium.layout.${hash >>> 0}.${userId}`
}

function read(key: string, store: Storage): unknown {
  try {
    const raw = store.getItem(key)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

export function useLayout(userId: number, known: (id: PanelId) => boolean) {
  const [layout, setLayout] = useState<Layout>(() => {
    // ?layout=reset is a URL the operator can type at a blank screen, and it
    // works even when no bench code would otherwise run.
    const params = new URLSearchParams(window.location.search)
    if (params.get('layout') === 'reset') {
      try {
        sessionStorage.removeItem(TAB_KEY)
        localStorage.removeItem(seedKey(userId))
      } catch {
        /* storage may be unavailable; the default layout below still applies */
      }
      params.delete('layout')
      const qs = params.toString()
      window.history.replaceState({}, '', window.location.pathname + (qs ? `?${qs}` : ''))
      return defaultLayout()
    }
    const tab = read(TAB_KEY, sessionStorage)
    if (tab) return parseLayout(tab, known)
    return parseLayout(read(seedKey(userId), localStorage), known)
  })

  // One snapshot, not a stack: one extra field, and it covers the fat-finger
  // close at every width, which is the only undo anyone actually reaches for.
  const [undo, setUndo] = useState<Layout | null>(null)

  useEffect(() => {
    const write = () => {
      try {
        const blob = JSON.stringify(layout)
        sessionStorage.setItem(TAB_KEY, blob)
        localStorage.setItem(seedKey(userId), blob)
      } catch {
        /* quota or private mode: this session simply does not persist */
      }
    }
    const t = setTimeout(write, 250)
    // Dropping a seam and hitting ⌘W within a quarter second is the "done for
    // the day" gesture, and without a flush it loses the size just set.
    // pagehide rather than beforeunload: the latter is unreliable and blocks
    // the back-forward cache.
    const flush = () => write()
    window.addEventListener('pagehide', flush)
    document.addEventListener('visibilitychange', flush)
    return () => {
      clearTimeout(t)
      window.removeEventListener('pagehide', flush)
      document.removeEventListener('visibilitychange', flush)
    }
  }, [layout, userId])

  const mutate = useCallback((f: (l: Layout) => Layout) => {
    setLayout((prev) => {
      setUndo(prev)
      return f(prev)
    })
  }, [])

  const slotOf = useCallback(
    (id: PanelId): SlotId | null => SLOT_ORDER.find((s) => layout.slots[s].panels.includes(id)) ?? null,
    [layout],
  )

  const api = {
    layout,
    slotOf,
    canUndo: undo !== null,

    /** Activating a tab always activates it. No toggle-away: that inverts the
     *  idiom already used everywhere else here and leaves a tab strip with no
     *  way to switch tabs. */
    activate: (slot: SlotId, id: PanelId) =>
      mutate((l) => ({
        ...l,
        slots: { ...l.slots, [slot]: { ...l.slots[slot], active: id, open: true } },
      })),

    /** push or overlay — the whole of "slide out", on the same record. */
    setMode: (slot: SlotId, mode: 'push' | 'overlay') =>
      mutate((l) => ({ ...l, slots: { ...l.slots, [slot]: { ...l.slots[slot], mode, open: true } } })),

    toggleOpen: (slot: SlotId) =>
      mutate((l) => ({
        ...l,
        slots: { ...l.slots, [slot]: { ...l.slots[slot], open: !l.slots[slot].open } },
      })),

    resize: (slot: SlotId, size: number) =>
      setLayout((l) => ({ ...l, slots: { ...l.slots, [slot]: { ...l.slots[slot], size } } })),

    /** Moving a panel rewrites which slot lists it. Nothing is reparented and
     *  nothing is re-keyed, so the component instance survives the move. */
    move: (id: PanelId, to: SlotId) =>
      mutate((l) => {
        const slots = { ...l.slots }
        for (const s of SLOT_ORDER) {
          if (!slots[s].panels.includes(id)) continue
          const panels = slots[s].panels.filter((p) => p !== id)
          slots[s] = { ...slots[s], panels, active: slots[s].active === id ? (panels[0] ?? null) : slots[s].active }
        }
        slots[to] = {
          ...slots[to],
          panels: [...slots[to].panels, id],
          active: id,
          open: true,
        }
        return { ...l, slots }
      }),

    /** show restores a panel to where it last was, falling back to its home. */
    show: (id: PanelId, home: SlotId) =>
      mutate((l) => {
        const at = SLOT_ORDER.find((s) => l.slots[s].panels.includes(id))
        const slot = at ?? home
        const slots = { ...l.slots }
        if (!at) slots[slot] = { ...slots[slot], panels: [...slots[slot].panels, id] }
        slots[slot] = { ...slots[slot], active: id, open: true }
        return { ...l, slots, maximized: null }
      }),

    close: (id: PanelId) =>
      mutate((l) => {
        const slots = { ...l.slots }
        for (const s of SLOT_ORDER) {
          if (!slots[s].panels.includes(id)) continue
          const panels = slots[s].panels.filter((p) => p !== id)
          slots[s] = { ...slots[s], panels, active: slots[s].active === id ? (panels[0] ?? null) : slots[s].active }
        }
        return { ...l, slots, maximized: l.maximized === id ? null : l.maximized }
      }),

    maximize: (id: PanelId | null) => mutate((l) => ({ ...l, maximized: l.maximized === id ? null : id })),

    reset: () => mutate(() => defaultLayout()),

    undoLast: () =>
      setLayout((prev) => {
        if (!undo) return prev
        setUndo(null)
        return undo
      }),
  }

  return api
}

export type LayoutApi = ReturnType<typeof useLayout>
