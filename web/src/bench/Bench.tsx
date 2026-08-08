import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { LayoutApi } from './store'
import { DEFAULTS, MIN_MAIN, RAIL, SLOT_ORDER, fit, px, type PanelId, type SlotId } from './types'

export type PanelDef = {
  id: PanelId
  title: string
  /** where this panel goes when it is opened and has no remembered slot */
  home: SlotId
  canClose?: boolean
  /** the width below which this panel stops being usable, in px */
  minW?: number
  /**
   * onDemand panels restore their POSITION but not their contents.
   *
   * The terminal is the reason this exists. serveTerminal starts a fresh shell
   * per WebSocket upgrade with no resume, so a restored layout containing it
   * spawns a container on every single page load — and lies while doing it,
   * showing the panel exactly where it was with none of the scrollback, cwd or
   * running process. Measured, not theorised: four reloads, four containers.
   */
  restore?: 'live' | 'onDemand'
  node: ReactNode
}

/**
 * The bench.
 *
 * INVARIANT — the one comment here that must never be deleted: the panel array
 * below is never filtered and never re-keyed. A panel's slot is an inline
 * style, so every panel stays a direct child of .bench for its whole life.
 * Reparenting remounts the subtree, and TerminalPage's cleanup runs ws.close()
 * and term.dispose() — a reparent would silently kill the operator's shell,
 * and the bug would look like "the terminal randomly disconnects".
 */
export default function Bench({ panels, layout }: { panels: PanelDef[]; layout: LayoutApi }) {
  const ref = useRef<HTMLDivElement>(null)
  // The BENCH's own box, not the window's. Clamping a slot against window
  // width lets main + aux exceed the container, and the whole application
  // scrolls sideways — which is exactly how the chat ended up three words wide.
  const [vp, setVp] = useState({ w: 0, h: 0 })

  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => {
      const r = e.contentRect
      setVp({ w: Math.round(r.width), h: Math.round(r.height) })
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const byId = useMemo(() => new Map(panels.map((p) => [p.id, p])), [panels])

  // Which onDemand panels the operator has asked for IN THIS SESSION. Never
  // persisted: that is the entire point — the position comes back, the side
  // effect does not.
  const [started, setStarted] = useState<Set<PanelId>>(new Set())
  // Clamp at paint time, never on write: stored sizes are the operator's
  // intent, so plugging the monitor back in restores the real arrangement.
  const l = useMemo(() => fit(layout.layout, vp.w, vp.h), [layout.layout, vp])
  const max = l.maximized

  const overlayBox = (slot: SlotId): React.CSSProperties | null => {
    const d = l.slots[slot]
    if (max || d.mode !== 'overlay' || !d.open || d.panels.length === 0) return null
    if (slot === 'main' || slot === 'aux') return null
    const base: React.CSSProperties = { position: 'absolute', zIndex: 30 }
    if (slot === 'left') return { ...base, left: 0, top: 0, bottom: 0, width: d.size }
    if (slot === 'right') return { ...base, right: 0, top: 0, bottom: 0, width: d.size }
    if (slot === 'top') return { ...base, left: 0, right: 0, top: 0, height: d.size }
    return { ...base, left: 0, right: 0, bottom: 0, height: d.size }
  }

  const track = (slot: SlotId) => {
    const d = l.slots[slot]
    if (d.panels.length === 0) return 0 // no chrome and no seam for an empty slot
    if (max) return 0
    if (!d.open) return RAIL
    // An overlay keeps only its rail of tabs in the grid; the body floats above
    // the centre. Without this the track stays full width and the "overlay"
    // sits politely beside the thing it is supposed to cover.
    if (d.mode === 'overlay' && slot !== 'main' && slot !== 'aux') return RAIL
    return d.size
  }

  const mainActive = l.slots.main.active
  const minMain = Math.max(MIN_MAIN, (mainActive && byId.get(mainActive)?.minW) || 0)

  const style = {
    '--w-left': px(track('left'), 0),
    '--h-top': px(track('top'), 0),
    '--w-aux': px(track('aux'), 0),
    '--w-right': px(track('right'), 0),
    '--h-bottom': px(track('bottom'), 0),
    '--min-main': `${minMain}px`,
  } as React.CSSProperties

  // Sorted by fixed slot rank so DOM order matches reading order for tab
  // sequence and screen readers, without the array itself ever being reordered.
  const ordered = useMemo(() => {
    const rank = (id: PanelId) => {
      const s = SLOT_ORDER.findIndex((slot) => l.slots[slot].panels.includes(id))
      return s === -1 ? 99 : s
    }
    return [...panels].sort((a, b) => rank(a.id) - rank(b.id))
  }, [panels, l])

  return (
    <div className="bn-bench" ref={ref} style={style}>
      {SLOT_ORDER.map((slot) => {
        const d = l.slots[slot]
        if (d.panels.length === 0 || max) return null
        return <DockChrome key={slot} slot={slot} layout={layout} byId={byId} overlay={overlayBox(slot)} />
      })}

      {ordered.map((p) => {
        const floating = l.floats[p.id]
        const slot = SLOT_ORDER.find((s) => l.slots[s].panels.includes(p.id))
        const dock = slot ? l.slots[slot] : null
        // live = the active tab of an open dock, or the maximized panel.
        // Everything else is PARKED: still mounted, sized, and holding its
        // sockets and buffers, just moved off screen.
        const live = floating ? !max : max ? max === p.id : !!dock && dock.open && dock.active === p.id
        const gated = p.restore === 'onDemand' && !started.has(p.id)
        const box = slot && live ? overlayBox(slot) : null
        const overlay = !!box
        const overlayStyle = box ?? undefined
        return (
          <div
            key={p.id}
            data-panel={p.id}
            className={`bn-panel ${live ? '' : 'bn-parked'} ${overlay ? 'bn-overlay' : ''} ${floating ? 'bn-float' : ''}`}
            style={
              !live
                ? undefined
                : floating
                  ? {
                      position: 'fixed',
                      left: floating.x,
                      top: floating.y,
                      width: floating.w,
                      height: floating.collapsed ? 'var(--h-tabs)' : floating.h,
                      // z from array index, never a stored counter: a counter
                      // drifts upward forever and eventually puts a panel above
                      // the approval dialog.
                      zIndex: 40 + Object.keys(l.floats).indexOf(p.id),
                    }
                  : max
                    ? { gridArea: 'main', zIndex: 2 }
                    : (overlayStyle ?? { gridArea: slot })
            }
          >
            {floating && (
              <FloatBar
                title={p.title}
                rect={floating}
                onMove={(r) => layout.moveFloat(p.id, r)}
                onDock={() => layout.float(p.id, p.home)}
                onCollapse={() => layout.collapseFloat(p.id)}
                onExpand={() => layout.expandFloat(p.id, window.innerWidth, window.innerHeight)}
                onClose={() => layout.close(p.id)}
              />
            )}
            {floating && !floating.collapsed && (
              <FloatGrip rect={floating} onResize={(r) => layout.moveFloat(p.id, r)} />
            )}
            {floating?.collapsed ? null : gated ? (
              <div className="bn-gate">
                <p className="hint">
                  A shell is not reconnected automatically. Restoring your layout brings the panel back, not the
                  session — the previous one is gone, along with its scrollback and working directory.
                </p>
                <button className="primary" onClick={() => setStarted((s) => new Set(s).add(p.id))}>
                  start a shell
                </button>
              </div>
            ) : (
              p.node
            )}
          </div>
        )
      })}

      {!max && l.slots.aux.panels.length > 0 && l.slots.aux.open && vp.w > 0 && (
        <Splitter
          axis="x"
          cssVar="--w-aux"
          size={track('aux')}
          min={Math.max(240, (l.slots.aux.active && byId.get(l.slots.aux.active)?.minW) || 0)}
          max={Math.max(240, vp.w - track('left') - track('right') - minMain)}
          benchRef={ref}
          onCommit={(n) => layout.resize('aux', n)}
        />
      )}
      {!max && l.slots.left.panels.length > 0 && l.slots.left.open && l.slots.left.mode === 'push' && vp.w > 0 && (
        <Splitter
          axis="x"
          grows="positive"
          cssVar="--w-left"
          size={track('left')}
          min={Math.max(180, (l.slots.left.active && byId.get(l.slots.left.active)?.minW) || 0)}
          max={Math.max(180, vp.w - track('aux') - track('right') - minMain)}
          benchRef={ref}
          onCommit={(n) => layout.resize('left', n)}
        />
      )}
      {!max && l.slots.right.panels.length > 0 && l.slots.right.open && l.slots.right.mode === 'push' && vp.w > 0 && (
        <Splitter
          axis="x"
          cssVar="--w-right"
          size={track('right')}
          min={160}
          max={Math.max(160, vp.w - track('left') - track('aux') - minMain)}
          benchRef={ref}
          onCommit={(n) => layout.resize('right', n)}
        />
      )}
      {!max && l.slots.bottom.panels.length > 0 && l.slots.bottom.open && l.slots.bottom.mode === 'push' && vp.h > 0 && (
        <Splitter
          axis="y"
          cssVar="--h-bottom"
          size={track('bottom')}
          min={120}
          max={Math.max(120, vp.h - 160)}
          benchRef={ref}
          onCommit={(n) => layout.resize('bottom', n)}
        />
      )}
    </div>
  )
}

function clamp(n: number, lo: number, hi: number) {
  return Math.max(lo, Math.min(hi, n))
}

/** The title bar of a floating panel: drag handle, and the way back to a dock. */
function FloatBar({
  title,
  rect,
  onMove,
  onDock,
  onCollapse,
  onExpand,
  onClose,
}: {
  title: string
  rect: { x: number; y: number; w: number; h: number; collapsed?: boolean }
  onMove: (r: { x: number; y: number; w: number; h: number }) => void
  onDock: () => void
  onCollapse: () => void
  onExpand: () => void
  onClose: () => void
}) {
  const from = useRef<{ x: number; y: number; rx: number; ry: number } | null>(null)
  return (
    <div
      className="bn-floatbar"
      onPointerDown={(e) => {
        if ((e.target as HTMLElement).tagName === 'BUTTON') return
        e.currentTarget.setPointerCapture(e.pointerId)
        from.current = { x: e.clientX, y: e.clientY, rx: rect.x, ry: rect.y }
      }}
      onPointerMove={(e) => {
        if (!from.current || !e.currentTarget.hasPointerCapture(e.pointerId)) return
        const f = from.current
        // Re-clamped into the viewport so a float can never be dragged
        // somewhere it cannot be dragged back from.
        onMove({
          ...rect,
          x: clamp(f.rx + (e.clientX - f.x), 0, window.innerWidth - 120),
          y: clamp(f.ry + (e.clientY - f.y), 0, window.innerHeight - 40),
        })
      }}
      onPointerUp={(e) => {
        from.current = null
        e.currentTarget.releasePointerCapture(e.pointerId)
      }}
    >
      <strong>{title}</strong>
      <span className="bn-spacer" />
      <button className="bn-icon" onClick={onCollapse} title={rect.collapsed ? 'Unroll' : 'Roll up'}>
        {rect.collapsed ? '▸' : '▾'}
      </button>
      <button className="bn-icon" onClick={onExpand} title="Fill the screen, or go back">
        ⤢
      </button>
      <button className="bn-icon" onClick={onDock} title="Put it back in a dock">
        ⤓
      </button>
      <button className="bn-icon" onClick={onClose} title="Close">
        ×
      </button>
    </div>
  )
}

/** The corner grip. Same rule as the seams: write to the DOM while dragging,
 *  commit once on release, so a resize never re-renders the canvas or the
 *  terminal inside the panel on every frame. */
function FloatGrip({
  rect,
  onResize,
}: {
  rect: { x: number; y: number; w: number; h: number }
  onResize: (r: { x: number; y: number; w: number; h: number }) => void
}) {
  const from = useRef<{ x: number; y: number; w: number; h: number } | null>(null)
  return (
    <div
      className="bn-grip"
      title="Resize"
      onPointerDown={(e) => {
        e.stopPropagation()
        e.currentTarget.setPointerCapture(e.pointerId)
        from.current = { x: e.clientX, y: e.clientY, w: rect.w, h: rect.h }
      }}
      onPointerMove={(e) => {
        if (!from.current || !e.currentTarget.hasPointerCapture(e.pointerId)) return
        const f = from.current
        onResize({
          ...rect,
          w: Math.max(280, f.w + (e.clientX - f.x)),
          h: Math.max(160, f.h + (e.clientY - f.y)),
        })
      }}
      onPointerUp={(e) => {
        from.current = null
        e.currentTarget.releasePointerCapture(e.pointerId)
      }}
    />
  )
}

/**
 * The tab strip IS the panel header — one 30px row, not a stacked app header
 * plus a frame bar plus tabs. Competing designs stacked three and then
 * complained about canvas starvation.
 */
function DockChrome({
  slot,
  layout,
  byId,
  overlay,
}: {
  slot: SlotId
  layout: LayoutApi
  byId: Map<PanelId, PanelDef>
  overlay: React.CSSProperties | null
}) {
  const d = layout.layout.slots[slot]
  const [menuFor, setMenuFor] = useState<PanelId | null>(null)

  return (
    <div
      className={`bn-chrome bn-chrome-${slot} ${d.open ? '' : 'bn-rail'} ${overlay ? 'bn-chrome-overlay' : ''}`}
      // The tab strip travels WITH its panel. Computing the two separately is
      // how an overlaid panel ends up with its tabs stranded in the rail it
      // just left.
      style={overlay ? { ...overlay, bottom: 'auto', height: 'var(--h-tabs)', zIndex: 31 } : { gridArea: slot }}
    >
      <div className="bn-tabs">
        {d.panels.map((id) => {
          const p = byId.get(id)
          if (!p) return null
          return (
            <button
              key={id}
              className={`bn-tab ${d.active === id && d.open ? 'active' : ''}`}
              onClick={() => layout.activate(slot, id)}
              title={p.title}
            >
              {p.title}
            </button>
          )
        })}
      </div>
      <span className="bn-spacer" />
      <button className="bn-icon" onClick={() => layout.toggleOpen(slot)} title={d.open ? 'Collapse' : 'Expand'}>
        {d.open ? '–' : '+'}
      </button>
      {d.active && (
        <div className="bn-menu-holder">
          <button className="bn-icon" onClick={() => setMenuFor(menuFor ? null : d.active)} title="Place this panel">
            ⋯
          </button>
          {menuFor && (
            <PlaceMenu
              id={menuFor}
              slot={slot}
              layout={layout}
              def={byId.get(menuFor)}
              onDone={() => setMenuFor(null)}
            />
          )}
        </div>
      )}
    </div>
  )
}

const SLOT_LABEL: Record<SlotId, string> = {
  left: 'to left',
  top: 'to top',
  main: 'to centre',
  aux: 'beside centre',
  bottom: 'to bottom',
  right: 'to right',
}

/**
 * Placement is a menu, not a drag.
 *
 * A drag and a menu item reach the SAME outcomes. The drag costs a pointer
 * state machine, live drop-zone hit-testing, ghost chrome, edge-versus-centre
 * disambiguation on small groups, and a "where did it go" recovery story. The
 * menu is forty lines, reachable from the keyboard, announceable, and cannot
 * produce an intermediate broken state. It demos worse and works better.
 */
function PlaceMenu({
  id,
  slot,
  layout,
  def,
  onDone,
}: {
  id: PanelId
  slot: SlotId
  layout: LayoutApi
  def?: PanelDef
  onDone: () => void
}) {
  useEffect(() => {
    const close = () => onDone()
    // Deferred so the click that opened the menu does not immediately close it.
    const t = setTimeout(() => document.addEventListener('click', close), 0)
    return () => {
      clearTimeout(t)
      document.removeEventListener('click', close)
    }
  }, [onDone])

  return (
    <div className="bn-menu" onClick={(e) => e.stopPropagation()}>
      {SLOT_ORDER.map((s) => (
        <button
          key={s}
          onClick={() => {
            layout.move(id, s)
            onDone()
          }}
          disabled={s === slot}
        >
          {s === slot ? '✓ ' : '  '}
          {SLOT_LABEL[s]}
        </button>
      ))}
      <hr />
      {slot !== 'main' && slot !== 'aux' && (
        <button
          onClick={() => {
            layout.setMode(slot, layout.layout.slots[slot].mode === 'overlay' ? 'push' : 'overlay')
            onDone()
          }}
        >
          {layout.layout.slots[slot].mode === 'overlay' ? '✓ slide out over the centre' : '  slide out over the centre'}
        </button>
      )}
      <button
        onClick={() => {
          layout.float(id, def?.home ?? 'aux')
          onDone()
        }}
      >
        {'  float it'}
      </button>
      <button
        onClick={() => {
          layout.maximize(id)
          onDone()
        }}
      >
        maximize (⌘↵)
      </button>
      {def?.canClose !== false && (
        <button
          onClick={() => {
            layout.close(id)
            onDone()
          }}
        >
          close
        </button>
      )}
      <hr />
      <button onClick={() => { layout.undoLast(); onDone() }} disabled={!layout.canUndo}>
        undo layout change
      </button>
      <button onClick={() => { layout.reset(); onDone() }}>reset layout</button>
      <span className="hint">layout is saved per device</span>
    </div>
  )
}

/**
 * A seam.
 *
 * Two rules, and the first version of this got both wrong.
 *
 * ABSOLUTE, NOT INCREMENTAL. The size at pointerdown is captured once and
 * every move computes `startSize ± (now - startCoord)`. The first version fed
 * a delta-from-gesture-start into the size as it was RIGHT NOW, so each move
 * compounded on the last and the panel accelerated away from the pointer.
 *
 * SIGN COMES FROM GEOMETRY. A dock on the right or the bottom grows when the
 * pointer moves toward the origin, so its sign is negative; left and top grow
 * with it. The first version applied an `invert` flag AND subtracted the
 * result, and the two negations cancelled — the panel moved against the hand
 * dragging it.
 *
 * The drag writes the CSS variable straight to the DOM and commits to the
 * store once, on release. That is correctness rather than performance: a
 * store-driven drag re-renders the adjacent container, which IS the blueprint
 * canvas or xterm, and would refit it every frame instead of once.
 */
function Splitter({
  axis,
  cssVar,
  size,
  min,
  max,
  grows = 'negative',
  benchRef,
  onCommit,
}: {
  axis: 'x' | 'y'
  cssVar: string
  size: number
  min: number
  max: number
  /** which pointer direction makes this dock bigger */
  grows?: 'positive' | 'negative'
  benchRef: React.RefObject<HTMLDivElement | null>
  onCommit: (size: number) => void
}) {
  const drag = useRef<{ at: number; from: number; latest: number } | null>(null)
  const sign = grows === 'negative' ? -1 : 1

  const apply = (n: number) => {
    const clamped = clamp(n, min, max)
    benchRef.current?.style.setProperty(cssVar, `${clamped}px`)
    if (drag.current) drag.current.latest = clamped
    return clamped
  }

  return (
    <div
      className={`bn-seam bn-seam-${axis} bn-seam-${cssVar.replace('--', '')}`}
      role="separator"
      tabIndex={0}
      aria-orientation={axis === 'x' ? 'vertical' : 'horizontal'}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId)
        const at = axis === 'x' ? e.clientX : e.clientY
        drag.current = { at, from: size, latest: size }
      }}
      onPointerMove={(e) => {
        const d = drag.current
        if (!d || !e.currentTarget.hasPointerCapture(e.pointerId)) return
        const now = axis === 'x' ? e.clientX : e.clientY
        apply(d.from + (now - d.at) * sign)
      }}
      onPointerUp={(e) => {
        e.currentTarget.releasePointerCapture(e.pointerId)
        const d = drag.current
        drag.current = null
        if (d) onCommit(d.latest)
      }}
      onDoubleClick={() => onCommit(clamp(DEFAULTS_FOR(cssVar), min, max))}
      onKeyDown={(e) => {
        const step = 16
        const back = e.key === 'ArrowLeft' || e.key === 'ArrowUp'
        const fwd = e.key === 'ArrowRight' || e.key === 'ArrowDown'
        if (!back && !fwd) return
        e.preventDefault()
        onCommit(clamp(size + (back ? -step : step) * sign, min, max))
      }}
    />
  )
}

/** Double-clicking a seam returns it to the size it shipped with. */
function DEFAULTS_FOR(cssVar: string): number {
  if (cssVar === '--w-left') return DEFAULTS.left.size
  if (cssVar === '--w-aux') return DEFAULTS.aux.size
  if (cssVar === '--w-right') return DEFAULTS.right.size
  return DEFAULTS.bottom.size
}
