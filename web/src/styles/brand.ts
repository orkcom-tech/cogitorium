// Who made this, and where the mark came from.
//
// The ORKCOM design exists as one file — docs/images/orkcom.jpeg in dither-ork
// — dark ink on opaque white with roughly a third of the frame as margin. That
// is why dither-ork shipped its attribution as words: dropped into a dark bar,
// the file is a white rectangle with a small mark inside it.
//
// This is the same mark with its ink coverage moved into the alpha channel,
// box-filtered down from the full resolution and cropped to the glyph. Nothing
// was redrawn and nothing was re-proportioned, so it is the mark rather than an
// impression of it — and because the colour now lives in the alpha, it reads on
// any ground the operator's palette produces.
//
// Inlined rather than fetched, like the grain texture: this product ships as
// one binary and asks the network for nothing at runtime.
export const ORK_MARK =
  'data:image/png;base64,' +
  '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' +
  'AAAAAElFTkSuQmCC'

/** The org is the only ORKCOM address that resolves; there is no company site
 *  yet, and a hostname invented here would be a link that 404s. */
export const ORKCOM_URL = 'https://github.com/orkcom-tech'

/** The documentation site, built from docs/ by GitHub Pages. */
export const DOCS_URL = 'https://orkcom-tech.github.io/cogitorium/'

