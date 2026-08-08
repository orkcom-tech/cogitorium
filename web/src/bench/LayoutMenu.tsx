import { useEffect, useRef, useState } from 'react'
import type { LayoutApi } from './store'
import { PRESETS, loadSaved, storeSaved, type SavedLayout } from './presets'

/**
 * Presets and saved arrangements.
 *
 * The presets are named after the task rather than the geometry — nobody
 * thinks "I want a left dock and a bottom drawer", they think "I am wiring
 * this up". Saving stores the whole arrangement including the widths the
 * operator dragged, because that is what they are actually looking at when
 * they decide to keep it.
 */
export default function LayoutMenu({ layout }: { layout: LayoutApi }) {
  const [open, setOpen] = useState(false)
  const [saved, setSaved] = useState<SavedLayout[]>(() => loadSaved())
  const [naming, setNaming] = useState(false)
  const [name, setName] = useState('')
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false)
    }
    const t = setTimeout(() => document.addEventListener('click', close), 0)
    return () => {
      clearTimeout(t)
      document.removeEventListener('click', close)
    }
  }, [open])

  const save = () => {
    const trimmed = name.trim()
    if (!trimmed) return
    const next = [
      ...saved.filter((s) => s.name !== trimmed),
      { id: String(saved.length + 1) + '-' + trimmed, name: trimmed, layout: layout.layout },
    ]
    setSaved(next)
    storeSaved(next)
    setName('')
    setNaming(false)
  }

  return (
    <div className="bn-menu-holder" ref={box}>
      <button className="layout-chip" onClick={() => setOpen((v) => !v)} title="Layouts">
        layout ▾
      </button>
      {open && (
        <div className="bn-menu layout-menu">
          <span className="menu-head">Ready-made</span>
          {PRESETS.map((p) => (
            <button key={p.id} onClick={() => { layout.apply(p.build()); setOpen(false) }} title={p.why}>
              {p.name}
              <span className="muted"> — {p.why}</span>
            </button>
          ))}

          {saved.length > 0 && <span className="menu-head">Yours</span>}
          {saved.map((s) => (
            <div key={s.id} className="saved-row">
              <button onClick={() => { layout.apply(s.layout); setOpen(false) }}>{s.name}</button>
              <button
                className="bn-icon"
                title={`Forget "${s.name}"`}
                onClick={() => {
                  const next = saved.filter((x) => x.id !== s.id)
                  setSaved(next)
                  storeSaved(next)
                }}
              >
                ×
              </button>
            </div>
          ))}

          <hr />
          {naming ? (
            <form
              className="row"
              onSubmit={(e) => {
                e.preventDefault()
                save()
              }}
            >
              <input
                autoFocus
                className="grow"
                value={name}
                placeholder="name this arrangement"
                onChange={(e) => setName(e.target.value)}
              />
              <button className="primary" type="submit" disabled={!name.trim()}>
                save
              </button>
            </form>
          ) : (
            <button onClick={() => setNaming(true)}>save this arrangement…</button>
          )}
          <button onClick={() => { layout.undoLast(); setOpen(false) }} disabled={!layout.canUndo}>
            undo layout change
          </button>
          <button onClick={() => { layout.reset(); setOpen(false) }}>reset to default</button>
          <span className="hint">layouts are saved on this device</span>
        </div>
      )}
    </div>
  )
}
