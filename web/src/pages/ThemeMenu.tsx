import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { ACCENTS, applyTheme, loadTheme, saveTheme, type Theme } from '../styles/theme'

/**
 * Appearance: light or dark, and a colour.
 *
 * What this replaced was a grid of eleven finished looks. Two things were
 * wrong with it. The interface has one geometry now, so eleven palettes hung
 * on it were one design in eleven paint jobs rather than eleven designs. And
 * it asked the wrong question — nobody wants to decide between Nord and Bloom;
 * they want it dark at night, in a colour they like.
 *
 * The colour is not decoration here: every neutral in the palette is mixed
 * towards it, so the ground and the surfaces carry a little of it too. That is
 * what stops a chosen accent looking pasted onto somebody else's design.
 */
export default function ThemeMenu() {
  const [theme, setTheme] = useState<Theme>(loadTheme)
  const [open, setOpen] = useState(false)
  const [warning, setWarning] = useState('')

  useEffect(() => {
    applyTheme(theme)
    if (!saveTheme(theme)) setWarning('this browser refused to store the choice — it will be gone after a reload')
  }, [theme])

  useEffect(() => {
    if (!open) return
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('keydown', key)
    return () => document.removeEventListener('keydown', key)
  }, [open])

  return (
    <>
      <button
        className="rail-btn"
        // the bead paints itself from the current accent, so the control shows
        // what it does rather than saying it
        data-own
        onClick={() => setOpen((v) => !v)}
        title="Appearance"
      >
        <span className="swatch" style={{ background: theme.accent }} />
        <span className="sr-only">Appearance</span>
      </button>

      {open &&
        createPortal(
          <div className="modal-backdrop" onClick={() => setOpen(false)}>
            <div
              className="modal theme-dialog"
              role="dialog"
              aria-modal="true"
              aria-label="Appearance"
              onClick={(e) => e.stopPropagation()}
            >
              <div className="row theme-head">
                <h3>Appearance</h3>
                <span className="spacer" />
                <button data-own className="drawer-x" onClick={() => setOpen(false)} title="Close">
                  ×
                </button>
              </div>

              <p className="hint">
                Two choices. The mode decides the ground; the colour is yours, and everything else —
                the surfaces, the borders, the hover washes — is mixed towards it.
              </p>

              <span className="field-label">Light or dark</span>
              <div className="mode-row">
                {(['system', 'light', 'dark'] as const).map((m) => (
                  <button
                    key={m}
                    data-own
                    className={`mode-option ${theme.mode === m ? 'on' : ''}`}
                    aria-pressed={theme.mode === m}
                    onClick={() => setTheme((t) => ({ ...t, mode: m }))}
                  >
                    {m}
                  </button>
                ))}
              </div>
              <p className="hint">
                <strong>system</strong> follows the operating system and changes with it.
              </p>

              <span className="field-label">Colour</span>
              <div className="accent-row">
                {ACCENTS.map((a) => (
                  <button
                    key={a.hex}
                    data-own
                    className={`accent-option ${theme.accent.toLowerCase() === a.hex ? 'on' : ''}`}
                    style={{ background: a.hex }}
                    aria-pressed={theme.accent.toLowerCase() === a.hex}
                    title={a.name}
                    onClick={() => setTheme((t) => ({ ...t, accent: a.hex }))}
                  >
                    <span className="sr-only">{a.name}</span>
                  </button>
                ))}
                {/* Any colour, not only the eight. The eight are the ones known
                    to carry white text on the light ground and to read as text
                    on the dark one; a hand-picked hex is the operator's risk
                    and their business. */}
                <label className="accent-own" title="Any colour">
                  <input
                    type="color"
                    value={theme.accent}
                    onChange={(e) => setTheme((t) => ({ ...t, accent: e.target.value }))}
                  />
                  <span className="sr-only">Pick any colour</span>
                </label>
              </div>

              {warning && <p className="error">{warning}</p>}
              <p className="hint">the choice is saved on this device</p>
            </div>
          </div>,
          document.body,
        )}
    </>
  )
}
