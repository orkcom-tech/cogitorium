import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'

/**
 * Whether what is being rendered is inside a drawer.
 *
 * One thing depends on it: the title. The drawer head already names the panel,
 * on the frame, where every other label about the work lives — so a panel that
 * also prints its own heading puts the same word twice within forty pixels,
 * and reads it twice to a screen reader. Every drawer did this: "Gears" above
 * "Gears", "Terminal" above "Terminal".
 *
 * A context rather than a prop, because the alternative is threading one
 * boolean through every panel and every future one, and the panels that
 * forget it are exactly the ones nobody notices until the screenshot.
 */
const InDrawer = createContext(false)

/**
 * The panel's own title — printed on its own page, silent inside a drawer.
 */
export function PanelTitle({ children }: { children: ReactNode }) {
  // eslint-disable-next-line react-hooks/rules-of-hooks -- a plain read, no branch above it
  return useContext(InDrawer) ? null : <h2>{children}</h2>
}

/**
 * A drawer: it crawls out of an edge of the frame and floats over the cavity.
 *
 * What this replaced was a box that appeared at a measured offset below the
 * workspace's header — a header that no longer exists — with no motion of any
 * kind. It read as a dialog dropped on top of the work rather than as part of
 * the instrument, which is most of what separated this interface from the one
 * it is modelled on.
 *
 * Three things make the difference, and all three are in here:
 *
 *   IT COMES FROM SOMEWHERE. Closed, it is translated fully off its edge.
 *   Opening removes that transform, so it travels in on the expressive spatial
 *   curve — which overshoots slightly and settles — and the operator sees
 *   where it went when it leaves.
 *
 *   IT BELONGS TO AN EDGE, AND THE EDGE IS THE OPERATOR'S. Right by default
 *   for the things you take from, bottom for the things that happen over time,
 *   and any of the four by choice. The choice is remembered per drawer.
 *
 *   IT RESIZES FROM THE EDGE FACING THE WORK, and the size is remembered per
 *   drawer as well. No transition while dragging: a transition on something
 *   following the pointer reads as lag.
 */

export type Edge = 'left' | 'right' | 'top' | 'bottom'

const KEY = 'cogitorium.drawers'
const MIN = 240
const MAX_FRACTION = 0.8

type Saved = Record<string, { edge: Edge; size: number }>

function read(): Saved {
  try {
    const raw = localStorage.getItem(KEY)
    return raw ? (JSON.parse(raw) as Saved) : {}
  } catch {
    // A browser that refuses storage gets the defaults every time rather than
    // an interface that will not open.
    return {}
  }
}

function write(all: Saved) {
  try {
    localStorage.setItem(KEY, JSON.stringify(all))
  } catch {
    /* nothing to do about it, and losing a layout is not worth an error */
  }
}

/** Remembered placement for one drawer, with the caller's default. */
export function useDrawerPlacement(id: string, fallback: Edge, fallbackSize: number) {
  const [state, setState] = useState<{ edge: Edge; size: number }>(() => {
    const saved = read()[id]
    return saved ?? { edge: fallback, size: fallbackSize }
  })

  // Re-read when the id changes. One Drawer element serves every drawer, so it
  // does not remount when the operator opens a different one — and a useState
  // initialiser runs once. Without this the placement shown was whichever
  // drawer happened to be first, and a re-dock survived until the next reload
  // and then silently reverted.
  useEffect(() => {
    const saved = read()[id]
    setState(saved ?? { edge: fallback, size: fallbackSize })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  const put = useCallback(
    (next: Partial<{ edge: Edge; size: number }>) => {
      setState((prev) => {
        const merged = { ...prev, ...next }
        const all = read()
        all[id] = merged
        write(all)
        return merged
      })
    },
    [id],
  )

  return [state, put] as const
}

const VERTICAL = (e: Edge) => e === 'top' || e === 'bottom'

export function Drawer({
  id,
  open,
  title,
  defaultEdge = 'right',
  defaultSize = 380,
  onClose,
  children,
}: {
  /** Stable across mounts: it is the key the placement is remembered under. */
  id: string
  open: boolean
  title: string
  defaultEdge?: Edge
  defaultSize?: number
  onClose: () => void
  children: ReactNode
}) {
  const [{ edge, size }, put] = useDrawerPlacement(id, defaultEdge, defaultSize)
  const box = useRef<HTMLDivElement>(null)
  const drag = useRef<{ at: number; from: number } | null>(null)
  const [dragging, setDragging] = useState(false)

  // Mounted closed for one frame, then opened, so the browser has a start
  // state to animate FROM. Without it the element appears already in place and
  // the transition never runs — the same reason a CSS transition on a freshly
  // inserted node does nothing.
  const [shown, setShown] = useState(false)
  useEffect(() => {
    if (!open) {
      setShown(false)
      return
    }
    const r = requestAnimationFrame(() => setShown(true))
    return () => cancelAnimationFrame(r)
  }, [open])

  // The frame grew inward, so the hole must give up the room. Without this the
  // drawer simply covers the work — the composer ended up half under it — which
  // is the behaviour of a thing that floats above, and the whole point is that
  // this one does not. Written as custom properties rather than as a class so
  // the size can follow the grip while it is being dragged.
  useEffect(() => {
    const root = document.documentElement
    if (!open) {
      root.style.removeProperty('--drawer-edge')
      root.style.removeProperty('--drawer-size')
      return
    }
    root.style.setProperty('--drawer-edge', edge)
    root.style.setProperty('--drawer-size', `${size}px`)
    return () => {
      root.style.removeProperty('--drawer-edge')
      root.style.removeProperty('--drawer-size')
    }
  }, [open, edge, size])

  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent) => {
      const t = e.target as HTMLElement
      if (box.current?.contains(t)) return
      // The rail button that owns this drawer handles its own click; closing
      // here as well would toggle it shut and straight back open.
      if (t.closest?.('.rail')) return
      // A control inside this drawer may render part of itself on <body> — a
      // dropdown's list is fixed to the viewport because the panel it sits in
      // clips. That is not somebody clicking away: picking a gear to grant used
      // to close the whole panel before the grant could be pressed.
      if (t.closest?.('[data-portal]')) return
      onClose()
    }
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    const t = setTimeout(() => document.addEventListener('mousedown', away), 0)
    document.addEventListener('keydown', key)
    return () => {
      clearTimeout(t)
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', key)
    }
  }, [open, onClose])

  if (!open) return null

  const vertical = VERTICAL(edge)
  const limit = (vertical ? window.innerHeight : window.innerWidth) * MAX_FRACTION
  const style = vertical ? { height: size } : { width: size }

  return (
    <aside
      ref={box}
      className={`drawer drawer-${edge} ${shown ? 'open' : ''} ${dragging ? 'dragging' : ''}`}
      role="dialog"
      aria-label={title}
      style={style}
    >
      <header className="drawer-head">
        <strong>{title}</strong>
        <span className="drawer-spacer" />
        {/* Which edge this comes out of is the operator's choice, and it is
            made here rather than in a settings screen: the control belongs
            beside the thing it moves. */}
        <span className="drawer-edges" role="group" aria-label="Dock to">
          {(['left', 'top', 'bottom', 'right'] as Edge[]).map((e) => (
            <button
              key={e}
              data-own
              className={`drawer-edge ${edge === e ? 'on' : ''}`}
              title={`Dock ${e}`}
              aria-pressed={edge === e}
              onClick={() => put({ edge: e })}
            >
              <span className={`edge-mark edge-${e}`} aria-hidden />
              <span className="sr-only">{`Dock ${e}`}</span>
            </button>
          ))}
        </span>
        <button className="drawer-x" data-own onClick={onClose} title="Close">
          ×
        </button>
      </header>

      <div className="drawer-body">
        <InDrawer.Provider value={true}>{children}</InDrawer.Provider>
      </div>

      {/* The grip is on the edge facing the work, which is the only edge that
          can grow. Written straight to state on release; while the pointer is
          down the element carries `dragging`, which turns the transition off so
          it tracks the hand instead of chasing it. */}
      <div
        className="drawer-grip"
        title="Resize"
        onPointerDown={(e) => {
          e.currentTarget.setPointerCapture(e.pointerId)
          drag.current = { at: vertical ? e.clientY : e.clientX, from: size }
          setDragging(true)
        }}
        onPointerMove={(e) => {
          const g = drag.current
          if (!g || !e.currentTarget.hasPointerCapture(e.pointerId)) return
          const delta = (vertical ? e.clientY : e.clientX) - g.at
          // Growing means moving AWAY from the drawer's own edge, so the sign
          // flips for the two edges whose origin is the far side.
          const towards = edge === 'right' || edge === 'bottom' ? -1 : 1
          const next = Math.min(Math.max(g.from + delta * towards, MIN), limit)
          const el = e.currentTarget.parentElement as HTMLElement
          if (vertical) el.style.height = `${next}px`
          else el.style.width = `${next}px`
          // The cavity follows the grip too, or the work jumps at the end of
          // the drag instead of moving with it.
          document.documentElement.style.setProperty('--drawer-size', `${next}px`)
        }}
        onPointerUp={(e) => {
          e.currentTarget.releasePointerCapture(e.pointerId)
          const el = e.currentTarget.parentElement as HTMLElement
          const done = vertical ? el.getBoundingClientRect().height : el.getBoundingClientRect().width
          drag.current = null
          setDragging(false)
          put({ size: Math.round(done) })
        }}
      />
    </aside>
  )
}
