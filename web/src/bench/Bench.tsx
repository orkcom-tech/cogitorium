import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { LayoutApi } from './store'
import { MIN_MAIN, RAIL, SLOT_ORDER, fit, px, type PanelId, type SlotId } from './types'

export type PanelDef = {
  id: PanelId
  title: string
  /** where this panel goes when it is opened and has no remembered slot */
  home: SlotId
  canClose?: boolean
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
  const [vp, setVp] = useState({ w: window.innerWidth, h: window.innerHeight })

  useEffect(() => {
    const onResize = () => setVp({ w: window.innerWidth, h: window.innerHeight })
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
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

  const track = (slot: SlotId) => {
    const d = l.slots[slot]
    if (d.panels.length === 0) return 0 // no chrome and no seam for an empty slot
    if (max) return 0
    return d.open ? d.size : RAIL
  }

  const style = {
    '--w-aux': px(track('aux'), 0),
    '--w-right': px(track('right'), 0),
    '--h-bottom': px(track('bottom'), 0),
    '--min-main': `${MIN_MAIN}px`,
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
        return <DockChrome key={slot} slot={slot} layout={layout} byId={byId} />
      })}

      {ordered.map((p) => {
        const slot = SLOT_ORDER.find((s) => l.slots[s].panels.includes(p.id))
        const dock = slot ? l.slots[slot] : null
        // live = the active tab of an open dock, or the maximized panel.
        // Everything else is PARKED: still mounted, sized, and holding its
        // sockets and buffers, just moved off screen.
        const live = max ? max === p.id : !!dock && dock.open && dock.active === p.id
        const gated = p.restore === 'onDemand' && !started.has(p.id)
        return (
          <div
            key={p.id}
            data-panel={p.id}
            className={`bn-panel ${live ? '' : 'bn-parked'}`}
            style={
              live
                ? max
                  ? { gridArea: 'main', zIndex: 2 }
                  : { gridArea: slot }
                : undefined
            }
          >
            {gated ? (
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

      {!max && l.slots.aux.panels.length > 0 && l.slots.aux.open && (
        <Splitter axis="x" invert onResize={(d) => layout.resize('aux', clamp(l.slots.aux.size - d, 240, vp.w - MIN_MAIN))} />
      )}
    </div>
  )
}

function clamp(n: number, lo: number, hi: number) {
  return Math.max(lo, Math.min(hi, n))
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
}: {
  slot: SlotId
  layout: LayoutApi
  byId: Map<PanelId, PanelDef>
}) {
  const d = layout.layout.slots[slot]
  const [menuFor, setMenuFor] = useState<PanelId | null>(null)

  return (
    <div className={`bn-chrome bn-chrome-${slot} ${d.open ? '' : 'bn-rail'}`} style={{ gridArea: slot }}>
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
 * A seam drags by writing a CSS variable straight to the DOM and commits ONCE
 * on release.
 *
 * This is a correctness requirement rather than an optimisation: a
 * store-driven drag re-renders the adjacent container, which is the blueprint
 * canvas or xterm — precisely the things the bench exists to keep alive — and
 * would fire a refit every frame instead of once.
 */
function Splitter({ axis, invert, onResize }: { axis: 'x' | 'y'; invert?: boolean; onResize: (delta: number) => void }) {
  const start = useRef(0)
  return (
    <div
      className={`bn-seam bn-seam-${axis}`}
      role="separator"
      tabIndex={0}
      aria-orientation={axis === 'x' ? 'vertical' : 'horizontal'}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId)
        start.current = axis === 'x' ? e.clientX : e.clientY
      }}
      onPointerMove={(e) => {
        if (!e.currentTarget.hasPointerCapture(e.pointerId)) return
        const now = axis === 'x' ? e.clientX : e.clientY
        onResize(invert ? start.current - now : now - start.current)
      }}
      onPointerUp={(e) => e.currentTarget.releasePointerCapture(e.pointerId)}
      onKeyDown={(e) => {
        const step = 16
        if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') onResize(invert ? step : -step)
        if (e.key === 'ArrowRight' || e.key === 'ArrowDown') onResize(invert ? -step : step)
      }}
    />
  )
}
