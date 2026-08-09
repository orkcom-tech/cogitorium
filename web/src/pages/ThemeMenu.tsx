import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { DEFAULT_THEME, PRESET_THEMES, applyTheme, loadTheme, saveTheme, withLook, type Theme } from '../styles/theme'

/**
 * The operator's palette: up to three colours and a grain dial.
 *
 * Only the ground and the accent are themed. Text and border tokens are
 * deliberately left on the light-dark() layer, so no palette anyone picks can
 * make their own interface unreadable — the failure mode of every "pick any
 * colour" feature that ships without a floor.
 */
export default function ThemeMenu() {
  const [theme, setTheme] = useState<Theme>(() => loadTheme())
  const [open, setOpen] = useState(false)
  const [bgError, setBgError] = useState<string | null>(null)

  useEffect(() => {
    applyTheme(theme)
    if (!saveTheme(theme)) {
      setBgError('this backdrop is too large to keep — it will be gone after a reload')
    }
  }, [theme])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  const setColor = (i: number, v: string) => {
    const colors = [...theme.colors]
    colors[i] = v
    setTheme({ ...theme, colors })
  }

  const stops = [theme.colors[0] ?? '#1a1a19', theme.colors[1] ?? '', theme.colors[2] ?? '']

  return (
    <>
      <button className="layout-chip theme-chip" onClick={() => setOpen((v) => !v)} title="Palette">
        <span className="swatch" style={{ background: `linear-gradient(135deg, ${stops.filter(Boolean).join(', ')})` }} />
        {/* A label, not a bare text node: a bare one cannot be hidden when the
            sidebar collapses, so it kept forcing the chip wider than the rail. */}
        <span className="chip-label">theme</span>
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
              <div className="theme-body">
          <span className="menu-head">Look</span>
          <div className="look-choice">
            {([
              ['instrument', 'Instrument', 'Dense. Hairlines, no rounding, figures in monospace. The conversation stays in the centre.'],
              ['canvas', 'Canvas-first', 'The wiring graph becomes the application; the conversation floats above it and the menu is a rail.'],
            ] as const).map(([v, name, why]) => (
              <button
                key={v}
                className={`look-option ${theme.look === v ? 'active' : ''}`}
                /* A look is a whole visual world — ground, accent, surface and
                   arrangement — so picking one lands you in it rather than
                   changing one attribute of the last one. Everything below
                   stays live: this is where you arrive, not where you stay. */
                onClick={() => setTheme(withLook(theme, v))}
              >
                <span className={`look-thumb look-${v}`} aria-hidden />
                <span className="look-text">
                  <strong>{name}</strong>
                  <span className="muted">{why}</span>
                </span>
              </button>
            ))}
          </div>

          <span className="menu-head">Ready-made</span>
          <div className="theme-presets">
            {PRESET_THEMES.map((p) => (
              <button
                key={p.name}
                className="theme-preset"
                title={p.name}
                onClick={() => setTheme({ ...theme, ...p.theme })}
              >
                <span
                  className="swatch lg"
                  style={{ background: `linear-gradient(135deg, ${p.theme.colors.join(', ')})` }}
                />
                {p.name}
              </button>
            ))}
          </div>

          <span className="menu-head">Your gradient</span>
          <div className="theme-stops">
            {[0, 1, 2].map((i) => (
              <label key={i} className="stop">
                <input
                  type="color"
                  value={stops[i] || '#222222'}
                  onChange={(e) => setColor(i, e.target.value)}
                  aria-label={`gradient colour ${i + 1}`}
                />
                <span className="muted">{i === 2 ? 'accent' : i === 0 ? 'base' : 'mid'}</span>
              </label>
            ))}
            {theme.colors.length > 1 && (
              <button
                className="bn-icon"
                title="Drop the last colour"
                onClick={() => setTheme({ ...theme, colors: theme.colors.slice(0, -1) })}
              >
                −
              </button>
            )}
          </div>

          <span className="menu-head">Background</span>
          <div className="row backdrop-row">
            <label className="file-btn">
              picture or clip…
              <input
                type="file"
                accept="image/*,video/mp4,video/webm"
                onChange={(e) => {
                  const f = e.target.files?.[0]
                  e.target.value = ''
                  if (!f) return
                  // Read locally and store as a data URL. Nothing is uploaded
                  // and nothing is ever fetched from a remote address.
                  const r = new FileReader()
                  r.onload = () => {
                    const data = String(r.result)
                    const kind = f.type.startsWith('video') ? 'video' : 'image'
                    try {
                      setTheme({ ...theme, bg: { ...theme.bg, kind, data } })
                      setBgError(null)
                    } catch {
                      setBgError('too large to keep')
                    }
                  }
                  r.readAsDataURL(f)
                }}
              />
            </label>
            {theme.bg.kind !== 'none' && (
              <button onClick={() => setTheme({ ...theme, bg: { ...theme.bg, kind: 'none', data: '' } })}>clear</button>
            )}
          </div>
          {bgError && <span className="hint warn">{bgError}</span>}
          {theme.bg.kind !== 'none' && (
            <label className="dial">
              dim it
              <input
                type="range"
                min={0}
                max={0.9}
                step={0.05}
                value={theme.bg.dim}
                onChange={(e) => setTheme({ ...theme, bg: { ...theme.bg, dim: Number(e.target.value) } })}
              />
            </label>
          )}

          <span className="menu-head">Panels</span>
          <div className="row mode-row">
            {(['glass', 'solid'] as const).map((v) => (
              <button key={v} className={theme.surface === v ? 'active' : ''} onClick={() => setTheme({ ...theme, surface: v })}>
                {v}
              </button>
            ))}
          </div>
          {theme.surface === 'glass' && (
            <label className="dial">
              blur
              <input
                type="range"
                min={0}
                max={40}
                step={2}
                value={theme.blur}
                onChange={(e) => setTheme({ ...theme, blur: Number(e.target.value) })}
              />
            </label>
          )}
          <label className="dial">
            {theme.surface === 'glass' ? 'darken' : 'lighten'}
            <input
              type="range"
              min={0.25}
              max={1}
              step={0.05}
              value={theme.dim}
              onChange={(e) => setTheme({ ...theme, dim: Number(e.target.value) })}
            />
          </label>

          <span className="menu-head">Light source</span>
          <GlowPad
            glow={theme.glow}
            disabled={theme.drift}
            onMove={(glow) => setTheme({ ...theme, glow })}
          />
          <label className="dial">
            <input
              type="checkbox"
              checked={theme.drift}
              onChange={(e) => setTheme({ ...theme, drift: e.target.checked })}
            />
            drift it around
          </label>
          {theme.drift && (
            <label className="dial">
              speed
              <input
                type="range"
                min={20}
                max={300}
                step={10}
                // Inverted: dragging right should mean faster, and faster is a
                // SHORTER circuit.
                value={320 - theme.driftSpeed}
                onChange={(e) => setTheme({ ...theme, driftSpeed: 320 - Number(e.target.value) })}
              />
            </label>
          )}

          <label className="dial">
            grain
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={theme.grain}
              onChange={(e) => setTheme({ ...theme, grain: Number(e.target.value) })}
            />
          </label>
          <label className="dial">
            tint
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={theme.tint}
              onChange={(e) => setTheme({ ...theme, tint: Number(e.target.value) })}
            />
          </label>

          <div className="row mode-row">
            {(['system', 'dark', 'light'] as const).map((m) => (
              <button key={m} className={theme.mode === m ? 'active' : ''} onClick={() => setTheme({ ...theme, mode: m })}>
                {m}
              </button>
            ))}
          </div>

          <hr />
          <button onClick={() => setTheme(DEFAULT_THEME)}>reset palette</button>
                <span className="hint">the palette is saved on this device</span>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </>
  )
}

/**
 * A miniature of the screen: drag inside it to put the light where you want.
 *
 * Disabled while the drift is on, because two authorities over one position is
 * how a control ends up fighting an animation and losing.
 */
function GlowPad({
  glow,
  disabled,
  onMove,
}: {
  glow: { x: number; y: number }
  disabled: boolean
  onMove: (g: { x: number; y: number }) => void
}) {
  const set = (e: React.PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect()
    onMove({
      x: Math.round(Math.max(0, Math.min(100, ((e.clientX - r.left) / r.width) * 100))),
      y: Math.round(Math.max(0, Math.min(100, ((e.clientY - r.top) / r.height) * 100))),
    })
  }
  return (
    <div
      className={`glow-pad ${disabled ? 'off' : ''}`}
      title={disabled ? 'Turn the drift off to place it by hand' : 'Drag to move the light'}
      onPointerDown={(e) => {
        if (disabled) return
        e.currentTarget.setPointerCapture(e.pointerId)
        set(e)
      }}
      onPointerMove={(e) => {
        if (disabled || !e.currentTarget.hasPointerCapture(e.pointerId)) return
        set(e)
      }}
      onPointerUp={(e) => e.currentTarget.releasePointerCapture(e.pointerId)}
    >
      <span className="glow-dot" style={{ left: `${glow.x}%`, top: `${glow.y}%` }} />
    </div>
  )
}
