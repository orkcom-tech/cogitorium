import { useCallback, useEffect, useState } from 'react'
import { session } from '../session'
import {
  MIN_FILES,
  MIN_OVERLAY_H,
  MIN_OVERLAY_W,
  MIN_TERMINAL,
  defaultDeck,
  parseDeck,
  type Deck,
  type ViewId,
} from './types'

// Persistence, split by lifetime — the one part of the old bench worth keeping.
//
// sessionStorage holds THIS tab's view; localStorage holds the seed a new tab
// starts from. The normal use of an agentic workbench is one tab watching a
// long run while you work in another, and a single shared key means the tab you
// are not looking at decides what the one you are looking at shows.

const TAB_KEY = 'cogitorium.deck'

// The seed key carries server and user. Workspace ids are SQLite rowids, so
// every server has a workspace 1; without the server segment a work server
// would inherit the home server's arrangement. Without the user segment,
// signing out would hand the next operator on a shared machine the previous
// one's bench.
function seedKey(userId: number): string {
  const server = session.server() || 'local'
  let hash = 0
  for (let i = 0; i < server.length; i++) hash = (hash * 31 + server.charCodeAt(i)) | 0
  return `cogitorium.deck.${hash >>> 0}.${userId}`
}

function read(key: string, store: Storage): unknown {
  try {
    const raw = store.getItem(key)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

export function useDeck(userId: number) {
  const [deck, setDeck] = useState<Deck>(() => {
    // ?layout=reset is a URL the operator can type at a blank screen, and it
    // works even when no other code here would run.
    const params = new URLSearchParams(window.location.search)
    if (params.get('layout') === 'reset') {
      try {
        sessionStorage.removeItem(TAB_KEY)
        localStorage.removeItem(seedKey(userId))
      } catch {
        /* storage may be unavailable; the default below still applies */
      }
      params.delete('layout')
      const qs = params.toString()
      window.history.replaceState({}, '', window.location.pathname + (qs ? `?${qs}` : ''))
      return defaultDeck()
    }
    const tab = read(TAB_KEY, sessionStorage)
    return parseDeck(tab ?? read(seedKey(userId), localStorage))
  })

  useEffect(() => {
    const write = () => {
      try {
        const blob = JSON.stringify(deck)
        sessionStorage.setItem(TAB_KEY, blob)
        localStorage.setItem(seedKey(userId), blob)
      } catch {
        /* quota or private mode: this session simply does not persist */
      }
    }
    const t = setTimeout(write, 250)
    // Dropping a seam and hitting ⌘W within a quarter second is the "done for
    // the day" gesture, and without a flush it loses the size just set.
    // pagehide rather than beforeunload: the latter is unreliable and blocks
    // the back-forward cache.
    const flush = () => write()
    window.addEventListener('pagehide', flush)
    document.addEventListener('visibilitychange', flush)
    return () => {
      clearTimeout(t)
      window.removeEventListener('pagehide', flush)
      document.removeEventListener('visibilitychange', flush)
    }
  }, [deck, userId])

  const go = useCallback((view: ViewId) => setDeck((d) => (d.view === view ? d : { ...d, view })), [])
  const toggleFiles = useCallback(() => setDeck((d) => ({ ...d, files: !d.files })), [])
  const toggleTerminal = useCallback(() => setDeck((d) => ({ ...d, terminal: !d.terminal })), [])
  const sizeFiles = useCallback(
    (n: number) => setDeck((d) => ({ ...d, filesW: Math.max(MIN_FILES, n) })),
    [],
  )
  const sizeTerminal = useCallback(
    (n: number) => setDeck((d) => ({ ...d, terminalH: Math.max(MIN_TERMINAL, n) })),
    [],
  )
  const sizeOverlay = useCallback(
    (w: number, h: number) =>
      setDeck((d) => ({
        ...d,
        overlayW: Math.max(MIN_OVERLAY_W, w),
        overlayH: Math.max(MIN_OVERLAY_H, h),
      })),
    [],
  )
  const reset = useCallback(() => setDeck(defaultDeck()), [])

  return { deck, go, toggleFiles, toggleTerminal, sizeFiles, sizeTerminal, sizeOverlay, reset }
}

export type DeckApi = ReturnType<typeof useDeck>
