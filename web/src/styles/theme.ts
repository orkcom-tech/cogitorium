// Appearance: a look, and a mode. That is the whole of it.
//
// What was here before: three gradient stops the operator picked by hand, a
// grain dial, a tint dial, a glass/solid switch, a blur radius, a "lighten"
// dial, a glow with an x, a y and a strength, a drift toggle with a speed, and
// a backdrop image or video. Fourteen settings, every one of which could be
// set to a combination that made the interface worse, and several of which
// could make text unreadable on a surface the operator had just tinted.
//
// A look is not a set of dials. It is a finished visual world — its ground,
// its accent, its corners, its idea of whether a panel has an edge or a
// shadow — authored once, in tokens, and drawn in both light and dark. Picking
// one is the whole interaction. There is nothing to tune afterwards because
// there is nothing left that an operator would improve by tuning.

export type Look =
  | 'shell'
  | 'calm'
  | 'paper'
  | 'terminal'
  | 'blueprint'
  | 'ember'
  | 'mono'
  | 'nord'
  | 'bloom'
  | 'slate'
  | 'contrast'
  | 'air'

export type Theme = {
  look: Look
  mode: 'dark' | 'light' | 'system'
}

/**
 * The catalogue, in the order it is offered.
 *
 * The blurb is what the operator reads, so it says what the look is FOR
 * rather than what colours it uses — they can see the colours. Every one of
 * these is drawn in both modes: a look that only works dark is half a look,
 * and `mode` stays the operator's in all twelve.
 */
export const LOOKS: { id: Look; name: string; blurb: string }[] = [
  {
    id: 'shell',
    name: 'Shell',
    blurb:
      'The default. A warm ground with a measured surface ladder on it, so a drawer floating over the work reads as a plane above it. Green means running, red means refused.',
  },
  { id: 'air', name: 'Air', blurb: 'A white sheet on a grey ground. Two colours only: green means running, red means broken.' },
  { id: 'calm', name: 'Calm', blurb: 'Rounded and quiet. Nothing asks for attention until something needs it.' },
  { id: 'slate', name: 'Slate', blurb: 'The neutral one. No character to get tired of, which is the character.' },
  { id: 'paper', name: 'Paper', blurb: 'Warm stock and thin rules. Nothing floats; everything is printed.' },
  { id: 'terminal', name: 'Terminal', blurb: 'Phosphor on carbon. Square, hairlined, and green when it is live.' },
  { id: 'blueprint', name: 'Blueprint', blurb: 'A drafting table. Indigo ground, cyan for anything drawn on it.' },
  { id: 'ember', name: 'Ember', blurb: 'A warm room. Deep charcoal, orange for state, generous corners.' },
  { id: 'mono', name: 'Mono', blurb: 'No hue anywhere. Only weight and edge can stand out.' },
  { id: 'nord', name: 'Nord', blurb: 'Cool and low contrast on purpose. For a long session in a dim room.' },
  { id: 'bloom', name: 'Bloom', blurb: 'Light, airy, violet. The decorative one, and the only graded ground.' },
  { id: 'contrast', name: 'Contrast', blurb: 'Built for legibility. Real borders, pure grounds, no shadow to muddy an edge.' },
]

const IDS = new Set<string>(LOOKS.map((l) => l.id))

// Shell is the look the interface is designed around — the frame, the rail and
// the drawers were drawn against its surface ladder — so it is what a fresh
// install opens in. Anyone with a stored theme keeps theirs: loadTheme reads
// what is there and only falls back per field.
export const DEFAULT_THEME: Theme = { look: 'shell', mode: 'light' }

const KEY = 'cogitorium.theme'

/**
 * loadTheme falls back per field and never throws.
 *
 * A stored theme from before this change carries a dozen fields nothing reads
 * any more, and possibly a `look` that no longer exists. Both are fine: the
 * extras are ignored and an unknown look lands on the default rather than on
 * an attribute nothing styles, which would render an unthemed page.
 */
export function loadTheme(): Theme {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return DEFAULT_THEME
    const t = JSON.parse(raw) as Partial<Theme>
    return {
      look: typeof t.look === 'string' && IDS.has(t.look) ? (t.look as Look) : DEFAULT_THEME.look,
      mode: t.mode === 'dark' || t.mode === 'light' ? t.mode : 'system',
    }
  } catch {
    return DEFAULT_THEME
  }
}

export function saveTheme(t: Theme): boolean {
  try {
    localStorage.setItem(KEY, JSON.stringify(t))
    return true
  } catch {
    return false
  }
}

/**
 * Apply the theme to the document — two attributes, and nothing else.
 *
 * Every colour, corner and shadow is a token in tokens.css under
 * `:root[data-look=…]`, so this function has no palette knowledge at all.
 * That is deliberate: the previous version wrote eighteen custom properties
 * from JavaScript, which meant the stylesheet could not be read on its own to
 * know what anything would look like.
 */
export function applyTheme(t: Theme) {
  const root = document.documentElement
  root.setAttribute('data-look', t.look)
  if (t.mode === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', t.mode)
}
