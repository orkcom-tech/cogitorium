import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { LOOKS, applyTheme, loadTheme, saveTheme, type Look, type Theme } from '../styles/theme'

/**
 * Appearance: pick a look, pick a mode, close it.
 *
 * What this replaced was fourteen controls — three colour wells, a grain dial,
 * a tint dial, a glass/solid switch, a blur radius, a lightness dial, a pad
 * for dragging a glow around with a strength and a drift speed, and a file
 * picker for a backdrop image or video. Every one of them could be set to a
 * combination that made the interface worse, several could put unreadable text
 * on a surface the operator had just tinted, and none of them was a decision
 * anybody wanted to make twice.
 *
 * Ten finished looks is the same expressiveness with none of the rope. Each
 * swatch is painted from the look's own tokens rather than from a copy of
 * them, so what the button shows and what the interface becomes cannot drift
 * apart — the failure this whole file was rebuilt to remove.
 */
export default function ThemeMenu() {
  const [theme, setTheme] = useState<Theme>(() => loadTheme())
  const [open, setOpen] = useState(false)
  const [warning, setWarning] = useState<string | null>(null)

  useEffect(() => {
    applyTheme(theme)
    if (!saveTheme(theme)) setWarning('this browser refused to store the choice — it will be gone after a reload')
    else setWarning(null)
  }, [theme])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  const current = LOOKS.find((l) => l.id === theme.look) ?? LOOKS[0]

  return (
    <>
      {/* A rail button like every other one on the frame. It used to be a wide
          chip carrying the word "theme", which on a 56px rail stuck out of the
          capsule it sits in and broke the column's shape. The bead is the label
          now: it is painted from the current look's own tokens, so the control
          shows what it does rather than saying it. */}
      <button
        className="rail-btn"
        // the bead paints itself, so it says so — see tokens.css
        data-own
        onClick={() => setOpen((v) => !v)}
        title="Appearance"
      >
        <span className="swatch" data-look={theme.look} />
        <span className="sr-only">Appearance</span>
      </button>
      {open &&
        createPortal(
          <div className="modal-backdrop" onClick={() => setOpen(false)}>
            <div className="modal theme-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
              <div className="row theme-head">
                <h3>Appearance</h3>
                <span className="spacer" />
                <button onClick={() => setOpen(false)} title="Close">
                  ×
                </button>
              </div>

              <div className="look-grid">
                {LOOKS.map((l) => (
                  <button
                    key={l.id}
                    className={`look-option ${theme.look === l.id ? 'on' : ''}`}
                    aria-pressed={theme.look === l.id}
                    onClick={() => setTheme((t) => ({ ...t, look: l.id }))}
                  >
                    <LookSwatch look={l.id} />
                    <span className="look-name">{l.name}</span>
                  </button>
                ))}
              </div>

              <p className="hint look-blurb">{current.blurb}</p>

              <p className="menu-head">LIGHT OR DARK</p>
              <div className="row mode-row">
                {(['system', 'light', 'dark'] as const).map((m) => (
                  <button
                    key={m}
                    className={theme.mode === m ? 'on' : ''}
                    aria-pressed={theme.mode === m}
                    onClick={() => setTheme((t) => ({ ...t, mode: m }))}
                  >
                    {m}
                  </button>
                ))}
              </div>
              <p className="hint">
                Every look is drawn in both. <strong>system</strong> follows the operating system, and changes with it.
              </p>

              {warning && <p className="error">{warning}</p>}
              <p className="hint">the choice is saved on this device</p>
            </div>
          </div>,
          document.body,
        )}
    </>
  )
}

/**
 * A miniature of the look, drawn from the look's own tokens.
 *
 * `data-look` on the swatch means the CSS resolves the same variables it will
 * resolve on the real interface, in the mode currently in force — so a swatch
 * cannot promise a colour the look does not have, and the light and dark
 * halves are both correct without a second table of preview colours.
 */
function LookSwatch({ look }: { look: Look }) {
  return (
    <span className="look-swatch" data-look={look} aria-hidden>
      <span className="look-swatch-ground" />
      <span className="look-swatch-card" />
      <span className="look-swatch-accent" />
    </span>
  )
}
