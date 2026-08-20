import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { DeckApi } from './store'
import {
  MIN_CENTRE,
  MIN_FILES,
  MIN_OVERLAY_H,
  MIN_OVERLAY_W,
  VIEW_ORDER,
  px,
  type OverlayId,
  type ViewId,
} from './types'

/**
 * The track.
 *
 * INVARIANT — the comment here that must never be deleted: every view is
 * mounted, at full size, for its whole life. The track is one row of
 * full-width cells and switching views moves the row; nothing is added,
 * removed, or reparented.
 *
 * That is not an animation trick, it is the correctness requirement. Hiding a
 * view with display:none makes xterm's FitAddon measure zero and React Flow's
 * fitView produce NaN; shrinking one to a rail refits xterm and permanently
 * destroys the scrollback structure, which expanding again does not undo. The
 * previous design parked panels at left:-100000px to dodge exactly this. A
 * track needs no such trick: an off-screen cell is still a full-size cell.
 */
export function Deck({ view, views }: { view: ViewId; views: { id: ViewId; node: ReactNode }[] }) {
  // The track is as long as the views it was GIVEN, in that order — it used to
  // be the host's three, which left no room for a view a plugin contributed.
  // Every cell is still mounted for its whole life, which is the invariant
  // above and the reason this is a track rather than a switch.
  const at = views.findIndex((v) => v.id === view)
  return (
    <div className="dk-viewport">
      {/* VERTICALLY, because the control that moves it is a vertical rail.
          Going down the rail sends the work down and brings the next view up
          from below; going back up reverses it. A horizontal slide driven by a
          vertical column reads as two unrelated motions. */}
      <div className="dk-track" style={{ transform: `translateY(${-Math.max(0, at) * 100}%)` }}>
        {views.map((v) => (
          <Slide key={v.id} live={v.id === view}>
            {v.node}
          </Slide>
        ))}
      </div>
    </div>
  )
}

/** A view that is off screen is still rendered and still sized — but it is not
 *  reachable by Tab, and a screen reader does not read three views at once. */
function Slide({ live, children }: { live: boolean; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.inert = !live
  }, [live])
  return (
    <div className="dk-slide" ref={ref} aria-hidden={!live}>
      {children}
    </div>
  )
}

/**
 * The workbench: a tree and the file it opens.
 *
 * The shell used to be its third part, along the bottom. It is a drawer now —
 * something pulled out over whatever you are doing and pushed back — and
 * keeping both would be two terminals with one session between them.
 *
 * The tree and the shell roll up to their own header rather than closing.
 * There is no "where did it go" state to recover from, and therefore no
 * arrangement worth naming, saving or restoring — which is the whole reason
 * the preset machinery this replaces is gone.
 */
export function Workbench({
  deck,
  files,
  editor,
  slot,
}: {
  deck: DeckApi
  files: ReactNode
  editor: ReactNode
  /** The strip a plugin may put above this screen. See StageSlot. */
  slot?: ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  const d = deck.deck
  return (
    <div
      className="dk-bench"
      ref={ref}
      style={
        {
          /* Always its width. It used to collapse to a 34px rail when
             d.files was false, and the control that set that flag was the
             header I removed — so the tree could shrink to a sliver with
             'FILES' broken across two characters and nothing able to open it
             again. The seam resizes it; leaving the stage closes it. */
          '--dk-files': px(d.filesW, 240),
        } as React.CSSProperties
      }
    >
      {/* No PartHead: the tree draws its own bar with its own name and its own
          actions, so this one printed "Files" a second time directly above it.
          Rolling the tree up is what the Editor stage is for — you leave it. */}
      <section className="dk-part dk-files">
        <div className="dk-part-body">{files}</div>
      </section>
      {d.files && (
        <Seam
          axis="x"
          cssVar="--dk-files"
          size={d.filesW}
          min={MIN_FILES}
          max={() => Math.max(MIN_FILES, (ref.current?.clientWidth ?? 0) - MIN_CENTRE)}
          grows="positive"
          hostRef={ref}
          onCommit={deck.sizeFiles}
        />
      )}

      <section className="dk-part dk-centre">
        {slot}
        {editor}
      </section>

    </div>
  )
}

/**
 * The shell is not reconnected automatically.
 *
 * serveTerminal starts a fresh shell per WebSocket upgrade with no resume, so
 * a workbench that mounts the terminal on load spawns a container on every
 * single page load — and lies while doing it, showing a panel with none of the
 * scrollback, cwd or running process. Measured, not theorised: four reloads,
 * four containers. So the terminal is behind a gate the operator opens.
 */

/**
 * An overlay: something you consult, not something you work in.
 *
 * Anchored under the button that opened it, above everything, and gone on the
 * next click outside. One at a time — two of these open at once is the tab
 * pile this design exists to remove, just floating.
 */
export function OverlayHost({
  open,
  title,
  deck,
  onClose,
  children,
}: {
  open: OverlayId | null
  title: string
  deck: DeckApi
  onClose: () => void
  children: ReactNode
}) {
  const box = useRef<HTMLDivElement>(null)
  const drag = useRef<{ x: number; y: number; w: number; h: number; lw: number; lh: number } | null>(null)
  // Where the panel's top edge goes, measured rather than assumed.
  //
  // It used to be `calc(var(--h-head) + …)`, and --h-head was never defined
  // anywhere in the stylesheet — so every overlay opened at the 46px fallback
  // and covered the header it was opened from, including the view tabs. A
  // constant standing in for a measurement is a bug waiting for a redesign to
  // change the thing it was guessing at.
  const [top, setTop] = useState(0)

  useEffect(() => {
    if (!open) return
    const place = () => {
      const head = box.current?.closest('.ws-shell')?.querySelector('.ws-head')
      const b = head?.getBoundingClientRect()
      setTop(b ? b.bottom + 8 : 76)
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [open])
  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent) => {
      const t = e.target as Node
      if (box.current?.contains(t)) return
      // A click on the header button that owns an overlay is that button's to
      // handle: closing here too would toggle it shut and straight back open.
      if ((t as HTMLElement).closest?.('[data-overlay-button]')) return
      onClose()
    }
    const t = setTimeout(() => document.addEventListener('mousedown', away), 0)
    return () => {
      clearTimeout(t)
      document.removeEventListener('mousedown', away)
    }
  }, [open, onClose])

  if (!open) return null
  const d = deck.deck
  return (
    <div
      className="dk-overlay"
      ref={box}
      role="dialog"
      aria-label={title}
      style={{ width: d.overlayW, height: d.overlayH, top }}
    >
      <div className="dk-overlay-head">
        <strong>{title}</strong>
        <span className="dk-spacer" />
        <button className="dk-icon" onClick={onClose} title="Close">
          ×
        </button>
      </div>
      <div className="dk-overlay-body">{children}</div>
      {/* The grip is bottom-LEFT because the box is anchored top-right: a
          bottom-right grip would be pinned against the edge it grows from and
          could only ever make the overlay smaller. Same rule as the seams —
          write to the DOM while dragging, commit once on release, so the panel
          inside is not re-rendered on every frame. */}
      <div
        className="dk-grip"
        title="Resize"
        onPointerDown={(e) => {
          e.currentTarget.setPointerCapture(e.pointerId)
          drag.current = { x: e.clientX, y: e.clientY, w: d.overlayW, h: d.overlayH, lw: d.overlayW, lh: d.overlayH }
        }}
        onPointerMove={(e) => {
          const g = drag.current
          if (!g || !e.currentTarget.hasPointerCapture(e.pointerId)) return
          const w = clamp(g.w - (e.clientX - g.x), MIN_OVERLAY_W, window.innerWidth - 32)
          const h = clamp(g.h + (e.clientY - g.y), MIN_OVERLAY_H, window.innerHeight - 96)
          g.lw = w
          g.lh = h
          if (box.current) {
            box.current.style.width = `${w}px`
            box.current.style.height = `${h}px`
          }
        }}
        onPointerUp={(e) => {
          e.currentTarget.releasePointerCapture(e.pointerId)
          const g = drag.current
          drag.current = null
          if (g) deck.sizeOverlay(g.lw, g.lh)
        }}
      />
    </div>
  )
}

function clamp(n: number, lo: number, hi: number) {
  return Math.max(lo, Math.min(hi, n))
}

/**
 * A seam.
 *
 * Two rules, and the first version of this got both wrong.
 *
 * ABSOLUTE, NOT INCREMENTAL. The size at pointerdown is captured once and
 * every move computes `startSize ± (now - startCoord)`. Feeding a
 * delta-from-gesture-start into the size as it is RIGHT NOW compounds, and the
 * panel accelerates away from the pointer.
 *
 * SIGN COMES FROM GEOMETRY. A part at the bottom grows when the pointer moves
 * toward the origin, so its sign is negative; one on the left grows with it.
 *
 * The drag writes the CSS variable straight to the DOM and commits to the
 * store once, on release. That is correctness rather than performance: a
 * store-driven drag re-renders the neighbouring container, which IS xterm, and
 * would refit it every frame instead of once.
 */
function Seam({
  axis,
  cssVar,
  size,
  min,
  max,
  grows = 'negative',
  hostRef,
  onCommit,
}: {
  axis: 'x' | 'y'
  cssVar: string
  size: number
  min: number
  max: () => number
  grows?: 'positive' | 'negative'
  hostRef: React.RefObject<HTMLDivElement | null>
  onCommit: (size: number) => void
}) {
  const drag = useRef<{ at: number; from: number; latest: number } | null>(null)
  const sign = grows === 'negative' ? -1 : 1

  return (
    <div
      className={`dk-seam dk-seam-${axis}`}
      role="separator"
      tabIndex={0}
      aria-orientation={axis === 'x' ? 'vertical' : 'horizontal'}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId)
        drag.current = { at: axis === 'x' ? e.clientX : e.clientY, from: size, latest: size }
      }}
      onPointerMove={(e) => {
        const d = drag.current
        if (!d || !e.currentTarget.hasPointerCapture(e.pointerId)) return
        const now = axis === 'x' ? e.clientX : e.clientY
        const n = clamp(d.from + (now - d.at) * sign, min, max())
        d.latest = n
        hostRef.current?.style.setProperty(cssVar, `${n}px`)
      }}
      onPointerUp={(e) => {
        e.currentTarget.releasePointerCapture(e.pointerId)
        const d = drag.current
        drag.current = null
        if (d) onCommit(d.latest)
      }}
      onKeyDown={(e) => {
        const back = e.key === 'ArrowLeft' || e.key === 'ArrowUp'
        const fwd = e.key === 'ArrowRight' || e.key === 'ArrowDown'
        if (!back && !fwd) return
        e.preventDefault()
        onCommit(clamp(size + (back ? -16 : 16) * sign, min, max()))
      }}
    />
  )
}

/** The view switcher and the overlay buttons, in the workspace header. */
export function DeckBar({
  view,
  onGo,
  overlay,
  onOverlay,
  overlays,
}: {
  view: ViewId
  onGo: (v: ViewId) => void
  overlay: OverlayId | null
  onOverlay: (o: OverlayId | null) => void
  overlays: { id: OverlayId; title: string }[]
}) {
  return (
    <>
      <div className="dk-views" role="tablist" aria-label="View">
        {VIEW_ORDER.map((id) => (
          <button
            key={id}
            role="tab"
            aria-selected={view === id}
            className={`dk-view-tab ${view === id ? 'active' : ''}`}
            onClick={() => onGo(id)}
          >
            {VIEW_TITLE[id]}
          </button>
        ))}
      </div>
      <div className="dk-overlay-buttons">
        {overlays.map((o) => (
          <button
            key={o.id}
            data-overlay-button
            className={overlay === o.id ? 'active' : ''}
            aria-expanded={overlay === o.id}
            onClick={() => onOverlay(overlay === o.id ? null : o.id)}
          >
            {o.title}
          </button>
        ))}
      </div>
    </>
  )
}

export const VIEW_TITLE: Record<ViewId, string> = {
  chat: 'Chat',
  blueprint: 'Blueprint',
  workbench: 'Editor',
}
