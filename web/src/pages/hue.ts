// A workspace's colour, resolved.
//
// Two rules, and the second is the one that matters. A workspace nobody has
// coloured still gets a colour — derived from its id, so it is stable and two
// neighbouring workspaces are never the same shade — but that colour is NOT
// written back. The moment an unset hue is persisted, "nobody chose this" and
// "somebody chose exactly this" become the same state, and an install can
// never again be told apart from one that was hand-tuned.
//
// Saturation and lightness live here rather than in the database on purpose:
// they have to move with the look and the mode. A stored #rrggbb picked
// against Calm-dark is unreadable on Paper-light, and there is no migration
// that can repair a colour somebody chose under different rules.

/** Ten hues, far enough apart that two workspaces never read as one. */
export const PALETTE = [264, 166, 212, 328, 44, 18, 140, 292, 190, 350]

/**
 * hueOf is the colour to draw a workspace in.
 *
 * The derived case walks the palette by id rather than hashing: consecutive
 * ids are the common case, and a hash would happily give workspace 4 and
 * workspace 5 two blues eight degrees apart.
 */
export function hueOf(w: { id: number; hue: number | null }): number {
  if (w.hue != null) return ((w.hue % 360) + 360) % 360
  return PALETTE[(w.id - 1 + PALETTE.length) % PALETTE.length]
}

/** Whether the colour is the operator's or merely the one we picked for them. */
export const isChosen = (w: { hue: number | null }) => w.hue != null

/** The fill, at the weight a surface wants. */
export const tint = (hue: number, pct: number) => `hsl(${hue} 68% 52% / ${pct}%)`

/** The ink, dark enough to read on a light ground and light enough on a dark
 *  one. light-dark() rather than two rules, so it follows the mode with no
 *  second declaration anywhere. */
export const ink = (hue: number) => `light-dark(hsl(${hue} 62% 34%), hsl(${hue} 70% 72%))`
