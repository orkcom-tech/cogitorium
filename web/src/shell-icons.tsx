import type { ReactNode } from 'react'

/**
 * The glyphs a screen hands the rail.
 *
 * They live beside the shell rather than inside a page because the rail draws
 * them: putting them in the page would mean the page knew how wide a rail
 * button is. Inline SVG for the same reason as everything else here — this
 * product asks the network for nothing at runtime, and an icon font would be
 * either a request or sixty kilobytes for a dozen glyphs.
 *
 * 24px box, 1.5px stroke, no fill, colour inherited, so a change of look
 * repaints them with everything else.
 */

const svg = (d: ReactNode) => (
  <svg viewBox="0 0 24 24" aria-hidden>
    {d}
  </svg>
)

/** Capsule 2 — what the cavity is showing. */
export const STAGE_ICON: Record<string, ReactNode> = {
  workspaces: svg(
    <>
      <rect x="3" y="3" width="7" height="7" rx="2" />
      <rect x="14" y="3" width="7" height="7" rx="2" />
      <rect x="3" y="14" width="7" height="7" rx="2" />
      <rect x="14" y="14" width="7" height="7" rx="2" />
    </>,
  ),
  chat: svg(
    <path d="M20 12.5c0 3.6-3.6 6.5-8 6.5a9.7 9.7 0 0 1-2.6-.35L4.5 20.5l1.1-3.3A6.7 6.7 0 0 1 4 12.5C4 8.9 7.6 6 12 6s8 2.9 8 6.5Z" />,
  ),
  blueprint: svg(
    <>
      <circle cx="5.5" cy="12" r="2.4" />
      <circle cx="18.5" cy="6.5" r="2.4" />
      <circle cx="18.5" cy="17.5" r="2.4" />
      <path d="M7.7 10.9 16.3 7.6M7.7 13.1l8.6 3.3" />
    </>,
  ),
  workbench: svg(
    <>
      <path d="M9 8.5 5.5 12 9 15.5M15 8.5 18.5 12 15 15.5" />
      <path d="M13.2 5.5 10.8 18.5" />
    </>,
  ),
  map: svg(
    <>
      <path d="M9 4 3 6.5v13L9 17l6 3 6-2.5v-13L15 7 9 4Z" />
      <path d="M9 4v13M15 7v13" />
    </>,
  ),
}

/** Capsule 3 — what crawls out over it. */
export const DRAWER_ICON: Record<string, ReactNode> = {
  agents: svg(
    <>
      <circle cx="9" cy="8.5" r="3.2" />
      <path d="M3.5 19.5c0-3 2.5-5 5.5-5s5.5 2 5.5 5" />
      <path d="M16 5.6a3.2 3.2 0 0 1 0 5.8M17.5 14.9c2 .6 3.3 2.3 3.3 4.6" />
    </>,
  ),
  mcp: svg(
    <>
      <path d="M9 3.5v5M15 3.5v5" />
      <rect x="6" y="8.5" width="12" height="6" rx="2" />
      <path d="M12 14.5v3.2a2.8 2.8 0 0 0 2.8 2.8h1.7" />
    </>,
  ),
  gears: svg(
    <>
      <circle cx="12" cy="12" r="3.2" />
      <path d="M12 3.4v3M12 17.6v3M3.4 12h3M17.6 12h3M5.9 5.9l2.1 2.1M16 16l2.1 2.1M18.1 5.9 16 8M8 16l-2.1 2.1" />
    </>,
  ),
  instructions: svg(
    <>
      <rect x="4.5" y="3.5" width="15" height="17" rx="2.5" />
      <path d="M8.5 8.5h7M8.5 12h7M8.5 15.5h4" />
    </>,
  ),
  memory: svg(
    <>
      <ellipse cx="12" cy="6.5" rx="7.5" ry="3" />
      <path d="M4.5 6.5v11c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3v-11" />
      <path d="M4.5 12c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3" />
    </>,
  ),
  inlets: svg(
    <>
      <path d="M3.5 12.5h6l1.5 2.5h2l1.5-2.5h6" />
      <path d="M5.4 6.2 3.5 12.5v4.3a1.7 1.7 0 0 0 1.7 1.7h13.6a1.7 1.7 0 0 0 1.7-1.7v-4.3L18.6 6.2a1.7 1.7 0 0 0-1.6-1.1H7a1.7 1.7 0 0 0-1.6 1.1Z" />
    </>,
  ),
  queue: svg(
    <>
      <path d="M4.5 7h11M4.5 12h11M4.5 17h7" />
      <path d="M18.5 15.2v4.3M16.4 17.3h4.2" />
    </>,
  ),
  // A history: earlier states behind the one you are on. Stacked cards rather
  // than a clock, because what this drawer holds is what the workflow WAS, not
  // when.
  versions: svg(
    <>
      <rect x="8.5" y="3.5" width="12" height="12" rx="2" />
      <path d="M5.5 6.5v11a2 2 0 0 0 2 2h9" />
      <path d="M3.5 9.5v8a2 2 0 0 0 2 2h6" />
    </>,
  ),
  terminal: svg(
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2.5" />
      <path d="M7.5 9.5 10 12l-2.5 2.5M12.5 15h4" />
    </>,
  ),
  // The space itself: layered sheets, because that is what a versioned
  // document store is — the same file, kept more than once.
  context: svg(
    <>
      <path d="M4.5 8.2 12 4.5l7.5 3.7L12 12 4.5 8.2Z" />
      <path d="M4.5 12.4 12 16.1l7.5-3.7M4.5 16.3 12 20l7.5-3.7" />
    </>,
  ),
  env: svg(
    <>
      <path d="M9.5 4.5c-2.2 0-3 1-3 3v2.2c0 1.3-.7 2-2 2.3 1.3.3 2 1 2 2.3V16.5c0 2 .8 3 3 3" />
      <path d="M14.5 4.5c2.2 0 3 1 3 3v2.2c0 1.3.7 2 2 2.3-1.3.3-2 1-2 2.3V16.5c0 2-.8 3-3 3" />
    </>,
  ),
}
