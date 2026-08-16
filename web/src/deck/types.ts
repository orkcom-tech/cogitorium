// The deck: three views on one track, and four overlays above it.
//
// This replaces a six-slot dock. The dock could express more arrangements and
// nobody could read the one they had: six panels shared a single side slot, so
// opening the blueprint, the queue and the variables stacked them into one tab
// strip that looked like windows nested inside a window, while the header went
// on listing them as separate things. Expressiveness was never the problem.
//
// Two rules replace it.
//
//   A VIEW is a place you go. Chat, blueprint, workbench — one on screen at a
//   time, side by side on a track that slides. Nothing overlaps, nothing
//   stacks, and the header always names exactly where you are.
//
//   An OVERLAY is a thing you consult. The roster, the receivers, the queue,
//   the variables — you open one, read it, and it goes away. None of them is
//   something you work IN, so none of them earns a permanent share of the
//   width.
//
// The workbench is the one view with parts, because editing genuinely is three
// things at once: the tree, the file, and a shell in the same directory. They
// collapse to a header rather than closing, so the arrangement has no state
// worth naming or saving.

export type ViewId = 'chat' | 'blueprint' | 'workbench'
export type OverlayId = 'agents' | 'agent' | 'inlets' | 'queue' | 'env'

export const VIEW_ORDER: ViewId[] = ['chat', 'blueprint', 'workbench']
export const OVERLAY_ORDER: OverlayId[] = ['agents', 'inlets', 'queue', 'env']

export type Deck = {
  v: 2
  view: ViewId
  /** the workbench's side tree and its shell, each open or rolled to a header */
  files: boolean
  terminal: boolean
  /** px along each one's axis */
  filesW: number
  terminalH: number
  /** the box every overlay opens at — one size for all of them, because they
   *  are read in the same corner and a per-overlay size is four settings for
   *  one habit */
  overlayW: number
  overlayH: number
}

export const MIN_FILES = 180
export const MIN_TERMINAL = 120
export const MIN_CENTRE = 360
export const MIN_OVERLAY_W = 300
export const MIN_OVERLAY_H = 200

export function defaultDeck(): Deck {
  return {
    v: 2,
    view: 'chat',
    files: true,
    terminal: false,
    filesW: 240,
    terminalH: 240,
    overlayW: 460,
    // 640 rather than 520. Every overlay opens with an explanation and a form
    // above the thing it is actually about, and at 520 the list below them was
    // off the bottom on a 900px window — the Variables overlay opened on three
    // stored variables and showed none of them. The CSS ceiling
    // (100vh - 9rem) still clamps this on a short window, so it costs nothing
    // there and uses the space that was already free on a normal one.
    overlayH: 640,
  }
}

/**
 * parseDeck is total, and falls back field by field rather than wholesale: a
 * stored value nobody can render costs that one field, never the arrangement.
 */
export function parseDeck(raw: unknown): Deck {
  const out = defaultDeck()
  if (!raw || typeof raw !== 'object') return out
  const src = raw as Record<string, unknown>
  if (typeof src.view === 'string' && (VIEW_ORDER as string[]).includes(src.view)) out.view = src.view as ViewId
  if (typeof src.files === 'boolean') out.files = src.files
  if (typeof src.terminal === 'boolean') out.terminal = src.terminal
  const num = (v: unknown, lo: number, d: number) =>
    typeof v === 'number' && Number.isFinite(v) && v >= lo ? v : d
  out.filesW = num(src.filesW, MIN_FILES, out.filesW)
  out.terminalH = num(src.terminalH, MIN_TERMINAL, out.terminalH)
  out.overlayW = num(src.overlayW, MIN_OVERLAY_W, out.overlayW)
  out.overlayH = num(src.overlayH, MIN_OVERLAY_H, out.overlayH)
  return out
}

/** px guards the render edge: one invalid var() invalidates the WHOLE
 *  declaration, so a bad number would collapse the grid rather than one gap. */
export function px(n: number, fallback: number): string {
  return `${Number.isFinite(n) && n >= 0 ? n : fallback}px`
}
