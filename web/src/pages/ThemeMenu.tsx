import { useEffect, useRef, useState } from 'react'
import { DEFAULT_THEME, PRESET_THEMES, applyTheme, loadTheme, saveTheme, type Theme } from '../styles/theme'

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
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    applyTheme(theme)
    saveTheme(theme)
  }, [theme])

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

  const setColor = (i: number, v: string) => {
    const colors = [...theme.colors]
    colors[i] = v
    setTheme({ ...theme, colors })
  }

  const stops = [theme.colors[0] ?? '#1a1a19', theme.colors[1] ?? '', theme.colors[2] ?? '']

  return (
    <div className="bn-menu-holder" ref={box}>
      <button className="layout-chip theme-chip" onClick={() => setOpen((v) => !v)} title="Palette">
        <span className="swatch" style={{ background: `linear-gradient(135deg, ${stops.filter(Boolean).join(', ')})` }} />
        theme
      </button>
      {open && (
        <div className="bn-menu theme-menu">
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
      )}
    </div>
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
