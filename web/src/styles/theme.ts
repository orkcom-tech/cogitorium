// The operator's own palette.
//
// Up to three colours make a gradient behind everything, and a grain texture
// sits over it — the Arc idea, applied to a workbench. The point is not
// decoration: with panels as cards on a tinted, grained ground, a different
// arrangement finally LOOKS different. Flat dark on flat dark is why swapping
// layouts read as "nothing changed".

export type Theme = {
  /** one, two or three stops; one stop is a flat tint */
  colors: string[]
  /** 0…1 */
  grain: number
  /** how strongly the gradient tints surfaces */
  tint: number
  mode: 'dark' | 'light' | 'system'
  /** where the glow sits, in percent of the viewport */
  glow: { x: number; y: number }
  /** drift it slowly around the screen, bouncing off the edges */
  drift: boolean
  /** seconds for one full circuit */
  driftSpeed: number
  /** how every panel, card and rail is filled */
  surface: 'glass' | 'solid'
  /** backdrop blur in px, glass only */
  blur: number
  /** how dark the fill is, 0…1 */
  dim: number
  /**
   * The interface's shape and density.
   *
   * `instrument` is a bench instrument: hairlines instead of cards, no corner
   * radius, figures in monospace so columns line up, and the conversation in
   * the centre. `canvas` inverts it — the wiring graph becomes the whole
   * application and the conversation floats above it.
   *
   * This is one setting rather than two because the pair is a decision about
   * how you work, not two sliders to reconcile.
   */
  look: 'instrument' | 'canvas'
  /** the operator's own backdrop: a picture or a looping clip */
  bg: { kind: 'none' | 'image' | 'video'; data: string; dim: number }
}

export type Palette = Pick<Theme, 'colors' | 'grain' | 'tint'>

export type Look = Theme['look']

/**
 * What a look actually is.
 *
 * A look is not a density switch. Each one is a whole visual world — its own
 * ground, its own accent, its own idea of what a panel is — so picking one
 * carries its palette and surface treatment with it. Rounding the corners
 * differently on the same dark blue is not a design, it is a preference pane.
 *
 * The palette controls stay live underneath: the signature is where you land,
 * not a cage. Pick a look, then change every colour in it if you like.
 */
export const LOOK_SIGNATURE: Record<Look, Palette & Pick<Theme, 'surface' | 'dim' | 'blur' | 'glow' | 'drift'>> = {
  // Near-black with a mint instrument accent, and a glow low enough to read
  // as the bezel of a lit panel rather than as weather.
  instrument: {
    colors: ['#0d0f11', '#151b1a', '#5ec8a0'],
    grain: 0.3,
    tint: 0.55,
    surface: 'solid',
    dim: 1,
    blur: 0,
    glow: { x: 50, y: 0 },
    drift: false,
  },
  // Cooler and deeper, with the accent pushed brighter: on a dot grid the
  // wires have to stay legible against the ground they cross.
  canvas: {
    colors: ['#101318', '#1a2230', '#6ee7b7'],
    grain: 0.4,
    tint: 0.45,
    surface: 'glass',
    dim: 0.82,
    blur: 16,
    glow: { x: 84, y: 6 },
    drift: false,
  },
}

/** Switching look replaces the whole visual world, not one attribute. */
export function withLook(t: Theme, look: Look): Theme {
  return { ...t, ...LOOK_SIGNATURE[look], look }
}

export const PRESET_THEMES: { name: string; theme: Palette }[] = [
  { name: 'Graphite', theme: { colors: ['#1a1a19', '#232326'], grain: 0.5, tint: 0.5 } },
  { name: 'Lime', theme: { colors: ['#171a14', '#1f2a17', '#cdfa50'], grain: 0.55, tint: 0.35 } },
  { name: 'Cobalt', theme: { colors: ['#12161f', '#1a2340', '#3b6cff'], grain: 0.45, tint: 0.4 } },
  { name: 'Ember', theme: { colors: ['#1c1513', '#2e1a14', '#ff7a45'], grain: 0.5, tint: 0.35 } },
  { name: 'Moss', theme: { colors: ['#131a17', '#1b2a24', '#4fd1a5'], grain: 0.5, tint: 0.35 } },
]

export const DEFAULT_THEME: Theme = {
  ...PRESET_THEMES[0].theme,
  mode: 'system',
  glow: { x: 12, y: 4 },
  drift: false,
  driftSpeed: 90,
  surface: 'glass',
  blur: 14,
  dim: 0.62,
  look: 'instrument',
  bg: { kind: 'none', data: '', dim: 0.55 },
}

const KEY = 'cogitorium.theme'

export function loadTheme(): Theme {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return DEFAULT_THEME
    const t = JSON.parse(raw) as Partial<Theme>
    const colors = Array.isArray(t.colors)
      ? t.colors.filter((c) => typeof c === 'string' && /^#[0-9a-f]{6}$/i.test(c)).slice(0, 3)
      : []
    return {
      colors: colors.length ? colors : DEFAULT_THEME.colors,
      grain: clamp01(typeof t.grain === 'number' ? t.grain : DEFAULT_THEME.grain),
      tint: clamp01(typeof t.tint === 'number' ? t.tint : DEFAULT_THEME.tint),
      mode: t.mode === 'dark' || t.mode === 'light' ? t.mode : 'system',
      glow: {
        x: clampPct(t.glow?.x, DEFAULT_THEME.glow.x),
        y: clampPct(t.glow?.y, DEFAULT_THEME.glow.y),
      },
      drift: t.drift === true,
      driftSpeed: typeof t.driftSpeed === 'number' && t.driftSpeed >= 10 && t.driftSpeed <= 600 ? t.driftSpeed : 90,
      surface: t.surface === 'solid' ? 'solid' : 'glass',
      blur: typeof t.blur === 'number' && t.blur >= 0 && t.blur <= 40 ? t.blur : DEFAULT_THEME.blur,
      dim: clamp01(typeof t.dim === 'number' ? t.dim : DEFAULT_THEME.dim),
      look: t.look === 'canvas' ? 'canvas' : 'instrument',
      bg: {
        kind: t.bg?.kind === 'image' || t.bg?.kind === 'video' ? t.bg.kind : 'none',
        // Only a data: URL is ever accepted. A remote address here would make
        // the interface fetch from somewhere on every load, which this project
        // does not do — and would leak that it was loaded, to whoever hosts it.
        data: typeof t.bg?.data === 'string' && t.bg.data.startsWith('data:') ? t.bg.data : '',
        dim: clamp01(typeof t.bg?.dim === 'number' ? t.bg.dim : 0.55),
      },
    }
  } catch {
    return DEFAULT_THEME
  }
}

/** Returns false when the browser refused to keep it — a picture can be
 *  bigger than the storage quota, and the operator has to be told rather than
 *  discovering it after a reload. */
export function saveTheme(t: Theme): boolean {
  try {
    localStorage.setItem(KEY, JSON.stringify(t))
    return true
  } catch {
    return false
  }
}

function clamp01(n: number) {
  return Math.max(0, Math.min(1, Number.isFinite(n) ? n : 0))
}

function clampPct(n: unknown, fallback: number) {
  return typeof n === 'number' && Number.isFinite(n) ? Math.max(0, Math.min(100, n)) : fallback
}

// A tiling SVG noise, inlined as a data URI. No external asset: the project
// ships one self-contained binary and fetches nothing at runtime.
export const GRAIN_URL =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='160' height='160'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='3' stitchTiles='stitch'/%3E%3CfeColorMatrix type='saturate' values='0'/%3E%3C/filter%3E%3Crect width='160' height='160' filter='url(%23n)' opacity='0.55'/%3E%3C/svg%3E\")"

/**
 * Apply the theme to the document.
 *
 * Only the ground and the accent are themed. Text and border tokens stay on
 * the light-dark() layer untouched, because a palette the operator picked
 * freely must not be able to make their own text unreadable.
 */
export function applyTheme(t: Theme) {
  const root = document.documentElement
  const [a, b, c] = t.colors
  const stops = [a, b ?? a, c ?? b ?? a]

  root.style.setProperty('--theme-a', stops[0])
  root.style.setProperty('--theme-b', stops[1])
  root.style.setProperty('--theme-c', stops[2])
  root.style.setProperty('--grain', String(t.grain * 0.09))
  root.style.setProperty('--grain-img', t.grain > 0 ? GRAIN_URL : 'none')

  // Surfaces borrow a little of the palette so cards sit ON the ground rather
  // than floating in unrelated grey. Capped hard: past a third, contrast with
  // the text tokens stops being guaranteed.
  const mix = Math.round(t.tint * 22)
  root.style.setProperty('--surface-tint', `${mix}%`)

  // The third stop, when there is one, is the accent — that is what makes a
  // palette feel chosen rather than merely applied.
  if (c) root.style.setProperty('--accent', c)
  else root.style.removeProperty('--accent')

  root.style.setProperty('--glow-x', `${t.glow.x}%`)
  root.style.setProperty('--glow-y', `${t.glow.y}%`)
  root.style.setProperty('--drift-speed', `${t.driftSpeed}s`)
  // The drift is a CSS animation rather than a requestAnimationFrame loop on
  // purpose: a background effect must not keep a core busy for the whole
  // session. The browser can run this off the main thread and it costs
  // nothing once started.
  root.classList.toggle('drifting', t.drift)

  // Glass or solid, and how much of each. Solid is a real opaque fill rather
  // than glass with the blur turned down: a backdrop-filter that blurs nothing
  // still costs a compositing layer per panel, and on a laptop that is a fan
  // spinning for no visible reason.
  root.classList.toggle('solid-surfaces', t.surface === 'solid')
  root.setAttribute('data-look', t.look)
  root.style.setProperty('--surface-blur', `${t.blur}px`)
  root.style.setProperty('--surface-dim', String(t.dim))

  const hasBg = t.bg.kind !== 'none' && !!t.bg.data
  root.classList.toggle('has-backdrop', hasBg)
  root.style.setProperty('--backdrop-dim', String(t.bg.dim))
  root.style.setProperty('--backdrop-img', t.bg.kind === 'image' && t.bg.data ? `url("${t.bg.data}")` : 'none')

  if (t.mode === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', t.mode)
}
