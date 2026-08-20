import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

/**
 * A select the application draws itself.
 *
 * A native <select> opens the operating system's own list — a different
 * typeface, a different corner radius, a different highlight, and on macOS a
 * panel that overlaps the control it came from. In an interface this deliberate
 * about its own surfaces that is the one control that visibly is not part of
 * it, and there is no styling that reaches inside a native popup to fix it.
 *
 * What is NOT given up in exchange, because these are the reasons people keep
 * the native one:
 *   - the button is a real <button> with aria-expanded and aria-activedescendant
 *   - the list is a listbox with option roles, so it is announced as a list
 *   - up, down, home, end, enter, escape and tab all behave as expected
 *   - typing a letter jumps to the next option starting with it
 *   - it closes on an outside click and on scroll of an ancestor
 *
 * It deliberately does NOT try to be a combo box. Nothing in this product has
 * a list long enough to need searching, and the moment it does, that is a
 * different control rather than an option on this one.
 */
export type Option = { value: string; label: string; disabled?: boolean }

export function Select({
  value,
  options,
  onChange,
  placeholder,
  className,
  required,
  'aria-label': ariaLabel,
}: {
  value: string
  options: Option[]
  onChange: (v: string) => void
  placeholder?: string
  /** passed to the wrapper, so a caller can still say `grow` in a flex row */
  className?: string
  /** blocks form submission while nothing is chosen, the way the native
   *  control did — a required picker that silently submits empty is a worse
   *  regression than an ugly one */
  required?: boolean
  'aria-label'?: string
}) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const [at, setAt] = useState<{ left: number; top: number; width: number } | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const typed = useRef({ text: '', at: 0 })

  const chosen = options.find((o) => o.value === value)

  useEffect(() => {
    if (!open) return
    setActive(Math.max(0, options.findIndex((o) => o.value === value)))
    const away = (e: MouseEvent) => {
      const t = e.target as Node
      // The list lives in a portal on <body>, so it is NOT inside the control's
      // own box any more — checking only the box would treat every click on an
      // option as a click outside and close the list before the choice landed.
      if (box.current?.contains(t) || list.current?.contains(t)) return
      setOpen(false)
    }
    // A scroll FOLLOWS the control now; it does not shut the list.
    //
    // Closing on scroll was a fix for the list being anchored to a control that
    // had moved out from under it — and it kept coming back as its own bug,
    // because opening a list can itself move something: a panel gains a
    // scrollbar, a card settles, and the listener sees a scroll and closes in
    // the same frame it opened. It looked intermittent, and it was, because it
    // depended on whether the thing around the control happened to scroll.
    //
    // Following costs one measurement per scroll and cannot misfire. The list
    // closes only when the control has actually left the viewport, which is
    // the case the old rule was really about.
    const follow = (e: Event) => {
      const t = e.target as Node
      if (list.current?.contains(t) || list.current === t) return
      const b = box.current?.getBoundingClientRect()
      if (!b) return
      if (b.bottom < 0 || b.top > window.innerHeight) {
        setOpen(false)
        return
      }
      setAt({ left: b.left, top: b.bottom + 4, width: b.width })
    }
    const t = setTimeout(() => document.addEventListener('mousedown', away), 0)
    window.addEventListener('scroll', follow, true)
    return () => {
      clearTimeout(t)
      document.removeEventListener('mousedown', away)
      window.removeEventListener('scroll', follow, true)
    }
  }, [open, options, value])

  // Keep the active option in view, by scrolling THE LIST rather than asking
  // the browser to scroll whatever it likes. scrollIntoView walks up and moves
  // the nearest scrollable ancestor, which here is the card — and that is what
  // was closing the dropdown the instant it opened.
  useEffect(() => {
    if (!open) return
    const l = list.current
    const el = l?.querySelector<HTMLElement>('[data-active="true"]')
    if (!l || !el) return
    const top = el.offsetTop
    const bottom = top + el.offsetHeight
    if (top < l.scrollTop) l.scrollTop = top
    else if (bottom > l.scrollTop + l.clientHeight) l.scrollTop = bottom - l.clientHeight
  }, [open, active])

  // Where to put the list.
  //
  // It is rendered in a portal, fixed to the viewport, because the control is
  // routinely inside a card with `overflow: hidden` — a workspace card, a
  // panel — and an absolutely positioned list inside one of those is simply
  // clipped away. Measured on open and on resize; a scroll closes it, so it
  // never has to follow the control.
  useLayoutEffect(() => {
    if (!open) {
      setAt(null)
      return
    }
    const place = () => {
      const b = box.current?.getBoundingClientRect()
      if (b) setAt({ left: b.left, top: b.bottom + 4, width: b.width })
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [open])

  const pick = (i: number) => {
    const o = options[i]
    if (!o || o.disabled) return
    onChange(o.value)
    setOpen(false)
  }

  const step = (d: number) => {
    let i = active
    for (let n = 0; n < options.length; n++) {
      i = (i + d + options.length) % options.length
      if (!options[i].disabled) break
    }
    setActive(i)
  }

  const onKey = (e: React.KeyboardEvent) => {
    if (!open && (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown')) {
      e.preventDefault()
      setOpen(true)
      return
    }
    if (!open) return
    if (e.key === 'Escape') {
      e.preventDefault()
      setOpen(false)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      step(1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      step(-1)
    } else if (e.key === 'Home') {
      e.preventDefault()
      setActive(options.findIndex((o) => !o.disabled))
    } else if (e.key === 'End') {
      e.preventDefault()
      for (let i = options.length - 1; i >= 0; i--) if (!options[i].disabled) { setActive(i); break }
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      pick(active)
    } else if (e.key === 'Tab') {
      setOpen(false)
    } else if (e.key.length === 1) {
      // Type-ahead, with the same one-second window a native list uses.
      const now = Date.now()
      typed.current.text = now - typed.current.at > 1000 ? e.key : typed.current.text + e.key
      typed.current.at = now
      const q = typed.current.text.toLowerCase()
      const hit = options.findIndex((o) => !o.disabled && o.label.toLowerCase().startsWith(q))
      if (hit >= 0) setActive(hit)
    }
  }

  return (
    <div className={`sel ${className ?? ''}`} ref={box}>
      {/* A hidden mirror of the value, so the browser's own form validation
          still refuses an empty required field. The native <select> gave this
          for free and the first cut of this component dropped it, which turned
          "you must pick an agent" into a request the server refuses after the
          fact. */}
      {required && (
        <input
          className="sel-required"
          tabIndex={-1}
          aria-hidden
          required
          value={value}
          onChange={() => {}}
        />
      )}
      <button
        type="button"
        // data-own says "this control states its own shape". See the note on
        // the blanket button rule in tokens.css: without it the rule's weight
        // strips the border this component asks for.
        data-own
        className="sel-btn"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={onKey}
      >
        <span className={chosen ? '' : 'sel-empty'}>{chosen?.label ?? placeholder ?? 'choose…'}</span>
        <span className="sel-caret" aria-hidden>
          ▾
        </span>
      </button>
      {open &&
        at &&
        createPortal(
          <div
            className="sel-list"
            role="listbox"
            ref={list}
            tabIndex={-1}
            // This list is on <body>, not inside whatever opened it. Anything
            // that closes itself when clicked outside — a drawer, a menu — has
            // to be able to tell "outside" from "a piece of me that had to be
            // rendered elsewhere". Choosing an option used to shut the whole
            // agent panel before the choice could be acted on.
            data-portal="select"
            style={{ left: at.left, top: at.top, minWidth: at.width }}
          >
            {options.map((o, i) => (
              <div
                key={o.value}
                role="option"
                aria-selected={o.value === value}
                aria-disabled={o.disabled}
                data-active={i === active}
                className={`sel-opt ${o.value === value ? 'on' : ''} ${o.disabled ? 'off' : ''}`}
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => {
                  // mousedown rather than click: the outside-click listener
                  // runs on mousedown, and a click handler would fire after
                  // the list had already been taken away.
                  e.preventDefault()
                  pick(i)
                }}
              >
                {o.label}
              </div>
            ))}
          </div>,
          document.body,
        )}
    </div>
  )
}
