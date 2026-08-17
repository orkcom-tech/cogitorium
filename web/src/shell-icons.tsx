import type { ReactNode } from 'react'

/**
 * The glyphs a workspace hands the rail.
 *
 * They live beside the shell rather than inside the workspace because the rail
 * draws them: putting them in the page would mean the page knew how wide a
 * rail button is. Inline SVG for the same reason everything else here is —
 * this product asks the network for nothing at runtime.
 *
 * 24px box, 1.5px stroke, no fill, colour inherited, so a look change repaints
 * them with everything else.
 */

export const STAGE_ICON: Record<string, ReactNode> = {
  /** Chat: the conversation with the team. */
  chat: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M20 12.5c0 3.6-3.6 6.5-8 6.5a9.7 9.7 0 0 1-2.6-.35L4.5 20.5l1.1-3.3A6.7 6.7 0 0 1 4 12.5C4 8.9 7.6 6 12 6s8 2.9 8 6.5Z" />
    </svg>
  ),
  /** Blueprint: agents as nodes, and the wires between them. */
  blueprint: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <circle cx="5.5" cy="12" r="2.4" />
      <circle cx="18.5" cy="6.5" r="2.4" />
      <circle cx="18.5" cy="17.5" r="2.4" />
      <path d="M7.7 10.9 16.3 7.6M7.7 13.1l8.6 3.3" />
    </svg>
  ),
  /** Editor: the files the agents are working in. */
  workbench: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M9 8.5 5.5 12 9 15.5M15 8.5 18.5 12 15 15.5" />
      <path d="M13.2 5.5 10.8 18.5" />
    </svg>
  ),
}

export const DRAWER_ICON: Record<string, ReactNode> = {
  /** Agents: the roster. */
  agents: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <circle cx="9" cy="8.5" r="3.2" />
      <path d="M3.5 19.5c0-3 2.5-5 5.5-5s5.5 2 5.5 5" />
      <path d="M16 5.6a3.2 3.2 0 0 1 0 5.8M17.5 14.9c2 .6 3.3 2.3 3.3 4.6" />
    </svg>
  ),
  /** Receivers: a door into this workspace from outside. */
  inlets: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M3.5 12.5h6l1.5 2.5h2l1.5-2.5h6" />
      <path d="M5.4 6.2 3.5 12.5v4.3a1.7 1.7 0 0 0 1.7 1.7h13.6a1.7 1.7 0 0 0 1.7-1.7v-4.3L18.6 6.2a1.7 1.7 0 0 0-1.6-1.1H7a1.7 1.7 0 0 0-1.6 1.1Z" />
    </svg>
  ),
  /** Queue: what is running, what is waiting. */
  queue: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M4.5 7h11M4.5 12h11M4.5 17h7" />
      <path d="M18.5 15.2v4.3M16.4 17.3h4.2" />
    </svg>
  ),
  /** Variables: names a gear is given when it runs. */
  env: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M9.5 4.5c-2.2 0-3 1-3 3v2.2c0 1.3-.7 2-2 2.3 1.3.3 2 1 2 2.3V16.5c0 2 .8 3 3 3" />
      <path d="M14.5 4.5c2.2 0 3 1 3 3v2.2c0 1.3.7 2 2 2.3-1.3.3-2 1-2 2.3V16.5c0 2-.8 3-3 3" />
    </svg>
  ),
}
