// Appearance: light or dark, and a colour that is yours.
//
// What was here before this: eleven finished "looks" — Air, Calm, Slate,
// Paper, Terminal, Blueprint, Ember, Mono, Nord, Bloom, Contrast — each with
// its own ground, accent, corner radii and shadow recipe, drawn in both modes.
// Twenty-two visual worlds to keep working, and every screen in the product
// had to be checked against all of them.
//
// They went for two reasons. They were legacy: this interface has ONE geometry
// now — a bezel, a rail, a cavity, a drawer — and eleven palettes hung on one
// geometry are not eleven designs, they are one design in eleven paint jobs.
// And they answered the wrong question. An operator does not want to choose
// between Nord and Bloom; they want it dark at night, and they want it to
// carry a colour they like.
//
// So: a mode, and an accent. The mode decides the ground. The accent is the
// operator's own, and the neutrals are mixed towards it rather than being pure
// grey — which is what stops a chosen colour looking pasted onto somebody
// else's design.

export type Theme = {
  mode: 'dark' | 'light' | 'system'
  /** The primary role, as a hex. Everything else is derived from it in CSS. */
  accent: string
}

/**
 * Colours offered as a starting point. Not a fixed set — the operator can type
 * any hex — but eight that are known to work, because each has to survive two
 * constraints at once: dark enough to carry white text as a filled button on
 * the light ground, and light enough to read as text on the dark one. An
 * arbitrary colour usually fails one of those, and the picker says which.
 */
export const ACCENTS: { name: string; hex: string }[] = [
  // #0a8f24 carried white text at 4.23:1 as a filled button — the default
  // accent, and the only one besides amber that missed the line. Nine points
  // of green take it to 4.7 and nothing about the hue changes.
  { name: 'Green', hex: '#0a8624' },
  { name: 'Teal', hex: '#0f766e' },
  { name: 'Blue', hex: '#2563c9' },
  { name: 'Indigo', hex: '#4f46e5' },
  { name: 'Violet', hex: '#7c3aed' },
  { name: 'Rose', hex: '#be3455' },
  // #a4650a carried white text at 4.23:1 as a filled button — the only one of
  // the eight that missed 4.5, and it missed it in both modes because a filled
  // primary is the accent in both. Two steps darker clears it.
  { name: 'Amber', hex: '#8a5406' },
  { name: 'Slate', hex: '#4a5568' },
]

// Teal, not green. The colour is not only the accent — every neutral is mixed
// towards it — so the default decides whether a fresh install reads warm or
// cool, and this one should read cool. It is also the most turquoise of the
// eight and measures 5.47:1 carrying white text as a filled button, which is
// comfortably past the 4.5 that stopped the old green being usable at all.
export const DEFAULT_THEME: Theme = { mode: 'system', accent: '#0f766e' }

const KEY = 'cogitorium.theme'
const HEX = /^#[0-9a-f]{6}$/i

/**
 * loadTheme falls back per field and never throws.
 *
 * A theme stored before this change carries a `look` nothing reads any more
 * and no accent at all. Both are fine: the extra field is ignored and the
 * missing one lands on the default, so nobody opens the app to an unpainted
 * page because of a choice they made last year.
 */
export function loadTheme(): Theme {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return DEFAULT_THEME
    const t = JSON.parse(raw) as Partial<Theme>
    return {
      mode: t.mode === 'dark' || t.mode === 'light' ? t.mode : 'system',
      accent: typeof t.accent === 'string' && HEX.test(t.accent) ? t.accent : DEFAULT_THEME.accent,
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
 * Apply it: one attribute, and one custom property.
 *
 * The mode is an attribute because `light-dark()` in the stylesheet resolves
 * against the element's used color-scheme, so setting the scheme is what
 * repaints the whole palette. Writing colours from here instead would mean the
 * stylesheet could no longer be read on its own to know what anything looks
 * like.
 *
 * The accent is the one value this function does write, because it is the
 * operator's and cannot be known at build time. Everything derived from it —
 * the tinted neutrals, the hover washes, the selection — is derived in CSS
 * with color-mix, not here.
 */
export function applyTheme(t: Theme) {
  const root = document.documentElement
  if (t.mode === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', t.mode)
  root.style.setProperty('--accent-chosen', t.accent)
  tellTheServer(t)
}

/**
 * The same choice, somewhere the SERVER can read it.
 *
 * Half this product's screens are rendered by the server, and the theme lived
 * only in localStorage — which the server cannot see. So choosing light in the
 * app left every server-rendered screen dark: two halves of one product
 * disagreeing about what it looks like, with nothing to explain it.
 *
 * A cookie rather than a request, because it must be in hand for the FIRST
 * paint of a full page load. localStorage stays the client's own copy: it is
 * read synchronously before React mounts, which a cookie parse could also do
 * but which there is no reason to change.
 */
function tellTheServer(t: Theme) {
  const value = encodeURIComponent(JSON.stringify({ mode: t.mode, accent: t.accent }))
  // A year, and Lax: it is a preference, not a credential, and it must survive
  // arriving from a link somebody sent.
  document.cookie = `cogitorium_look=${value}; path=/; max-age=31536000; SameSite=Lax`
}
