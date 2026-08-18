import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

/**
 * A button that opens a list of things to DO.
 *
 * WHAT THIS EXISTS TO REPLACE. The blueprint's "add a gear to this workspace"
 * was a <Select> — the component for holding a VALUE you can change — with the
 * whole sentence "+ gear — add one to this workspace (all agents)…" as its
 * placeholder. Measured, it rendered 391px wide against a 78px button beside
 * it, in a control that looks exactly like a text field, permanently showing a
 * sentence it never stops showing because there is no value to show instead.
 * It read as a filter. It is an action.
 *
 * That is not a styling problem and no amount of CSS fixes it: a picker
 * displays what is chosen, and this never chooses anything — it does something
 * and goes back to saying the same sentence. Two more of these exist ("add a
 * team…", "add to team…") and they want this control too.
 *
 * The list is drawn the way the rail's menus are, because it is the same
 * gesture: same material as the frame, one radius, and it grows out of the
 * control that was pressed rather than fading in somewhere near it. It is
 * anchored under its trigger rather than to the rail's edge, so all four
 * corners are rounded — it is not joined to anything.
 *
 * What it keeps from a real menu, because these are the reasons people can use
 * one: a real <button> with aria-expanded and aria-haspopup, a menu role with
 * menuitem children, arrows/Home/End to move, Enter to pick, Escape to close
 * and return focus to the trigger, an outside click to dismiss, and closing on
 * a scroll of anything above it rather than following the page.
 */
export type MenuItem = {
  value: string
  label: string
  /** the second line: what this one is, for a list where the names are terse */
  sub?: string
  disabled?: boolean
}

export function DropMenu({
  label,
  title,
  items,
  onPick,
  empty,
  heading,
  className = '',
}: {
  /** what the button says. Short: it is an action, not a sentence. */
  label: ReactNode
  /** the sentence goes here, where a sentence belongs */
  title: string
  items: MenuItem[]
  onPick: (value: string) => void
  /** what the menu says when there is nothing to pick */
  empty: string
  heading?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const [at, setAt] = useState<{ left: number; top: number; width: number } | null>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLDivElement>(null)

  // Measured after paint, before the browser shows the frame: a menu that
  // positions itself in an effect appears at 0,0 for one frame first.
  useLayoutEffect(() => {
    if (!open || !trigger.current) return
    const r = trigger.current.getBoundingClientRect()
    setAt({ left: r.left, top: r.bottom + 6, width: r.width })
  }, [open])

  // FOCUS MOVES INTO THE MENU when it opens, and this is not a nicety.
  // Without it the keys go on reaching the trigger: Escape did nothing at all
  // (the trigger has no handler for it) and ArrowDown re-opened an already
  // open menu instead of moving down it. Measured on the first cut, which is
  // why the list is focusable at all.
  useEffect(() => {
    if (open && at) list.current?.focus()
  }, [open, at])

  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent) => {
      const t = e.target as Node
      if (!trigger.current?.contains(t) && !list.current?.contains(t)) setOpen(false)
    }
    // Closed rather than repositioned on scroll. A menu that follows its
    // trigger down a scrolling panel is a menu you have to chase; one that
    // closes is one you reopen where you are.
    const shut = () => setOpen(false)
    document.addEventListener('mousedown', away)
    window.addEventListener('scroll', shut, true)
    window.addEventListener('resize', shut)
    return () => {
      document.removeEventListener('mousedown', away)
      window.removeEventListener('scroll', shut, true)
      window.removeEventListener('resize', shut)
    }
  }, [open])

  useEffect(() => {
    if (open) setActive(items.findIndex((i) => !i.disabled))
  }, [open, items])

  const pick = (i: MenuItem) => {
    if (i.disabled) return
    setOpen(false)
    trigger.current?.focus()
    onPick(i.value)
  }

  const move = (from: number, step: number) => {
    if (items.length === 0) return
    let n = from
    for (let tries = 0; tries < items.length; tries++) {
      n = (n + step + items.length) % items.length
      if (!items[n].disabled) return setActive(n)
    }
  }

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className={`drop-trigger ${className}`}
        title={title}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setOpen(true)
          } else if (e.key === 'Escape' && open) {
            // Belt as well as braces: focus should be in the list by now, and
            // if anything ever keeps it here Escape must still shut the menu.
            e.stopPropagation()
            setOpen(false)
          }
        }}
      >
        {label}
        <span className="drop-caret" aria-hidden>
          ▾
        </span>
      </button>

      {open &&
        at &&
        createPortal(
          <div
            ref={list}
            className="drop-menu"
            role="menu"
            style={{ left: at.left, top: at.top, minWidth: Math.max(at.width, 240) }}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                e.stopPropagation()
                setOpen(false)
                trigger.current?.focus()
              } else if (e.key === 'ArrowDown') {
                e.preventDefault()
                move(active, 1)
              } else if (e.key === 'ArrowUp') {
                e.preventDefault()
                move(active, -1)
              } else if (e.key === 'Home') {
                e.preventDefault()
                move(-1, 1)
              } else if (e.key === 'End') {
                e.preventDefault()
                move(0, -1)
              } else if (e.key === 'Enter' && items[active]) {
                e.preventDefault()
                pick(items[active])
              }
            }}
            tabIndex={-1}
          >
            {heading && <span className="drop-menu-label">{heading}</span>}
            {items.length === 0 ? (
              <p className="hint drop-empty">{empty}</p>
            ) : (
              items.map((i, n) => (
                <button
                  key={i.value}
                  type="button"
                  role="menuitem"
                  disabled={i.disabled}
                  className={n === active ? 'on' : ''}
                  onMouseEnter={() => setActive(n)}
                  onClick={() => pick(i)}
                >
                  {i.label}
                  {i.sub && <span className="sub">{i.sub}</span>}
                </button>
              ))
            )}
          </div>,
          document.body,
        )}
    </>
  )
}
