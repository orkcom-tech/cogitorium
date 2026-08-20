import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, type TerminalStatus } from '../api'
import { session } from '../session'
import { PanelTitle } from '../deck/Drawer'
import StageSlot from '../StageSlot'

// A shell.
//
// Where there is a sandbox it runs inside it, next to where gears run. Where
// there is not — the ordinary case of somebody running this on their own
// machine — it is that machine, as the account this server runs as. The status
// line says which, every time, because the two are not the same thing to type
// `rm` into and nobody should have to guess.
//
// It opens with the panel and it stays open. The session lives on the server
// and outlives this socket, so leaving the screen and coming back returns to
// the same shell, in the same directory, with what it printed while you were
// gone — the way the terminal in an editor behaves.

/**
 * The terminal's colours, taken from the page rather than assumed.
 *
 * The background was transparent and the foreground was never set, so xterm
 * used its own default — white. That is right on the two dark looks and
 * invisible on anything light: white on paper, and white on white in light
 * mode, which was already true before a light look existed and had simply
 * never been looked at.
 *
 * The text colour is read off the holder's COMPUTED colour rather than the
 * --text custom property, because a custom property's computed value is the
 * unresolved light-dark(...) token — reading it gives a string no terminal can
 * use.
 *
 * The ANSI sixteen are set for the same reason: a gear that prints in colour
 * hands back bright yellow on cream otherwise. These are darkened for a light
 * ground and left bright for a dark one.
 */
function terminalTheme(el: HTMLElement) {
  const fg = getComputedStyle(el).color
  const light = isLight(fg)
  return {
    // rgba(), not the 8-digit hex that was here: xterm paints .xterm-viewport
    // from this value and rendered #00000000 as opaque BLACK. Nobody saw it
    // because the only grounds were near-black — on paper it is a black slab.
    background: 'rgba(0, 0, 0, 0)',
    foreground: fg,
    cursor: fg,
    cursorAccent: light ? '#ffffff' : '#000000',
    selectionBackground: light ? 'rgba(0,0,0,0.16)' : 'rgba(255,255,255,0.22)',
    ...(light
      ? {
          black: '#2a2622', red: '#b3352c', green: '#3f7d38', yellow: '#8a6a12',
          blue: '#2f5fa8', magenta: '#8a4a86', cyan: '#2b7a80', white: '#5c574f',
          brightBlack: '#7c766c', brightRed: '#d0483c', brightGreen: '#4f9a46',
          brightYellow: '#a8811a', brightBlue: '#3a74c9', brightMagenta: '#a55ba0',
          brightCyan: '#35939b', brightWhite: '#2a2622',
        }
      : {}),
  }
}

/** Perceived lightness of a computed rgb() string, for choosing a set. */
function isLight(color: string): boolean {
  const m = color.match(/\d+(\.\d+)?/g)
  if (!m || m.length < 3) return false
  const [r, g, b] = m.slice(0, 3).map(Number)
  // The text is what was measured, so a LIGHT text means a dark ground.
  return 0.2126 * r + 0.7152 * g + 0.0722 * b < 128
}
/**
 * `described` means something above this already says what the shell is and
 * why it might not be here — the workspace drawer renders the server's own
 * panel for exactly that. Saying it twice, in two sets of words, is how two
 * descriptions start disagreeing.
 */
export default function TerminalPage({
  workspaceId,
  described,
}: {
  workspaceId?: number
  described?: boolean
}) {
  const [status, setStatus] = useState<TerminalStatus | null>(null)
  const [connected, setConnected] = useState(false)
  const holder = useRef<HTMLDivElement>(null)
  const socket = useRef<WebSocket | null>(null)

  useEffect(() => {
    api.terminal.status().then(setStatus).catch(() => setStatus(null))
  }, [])

  // The server-wide shell is an administrator's; a workspace shell belongs
  // to whoever may use that workspace.
  const allowed = workspaceId ? status?.available : status?.global_available
  const blockedReason = workspaceId ? status?.reason : status?.global_reason

  useEffect(() => {
    if (!allowed || !holder.current) return

    const term = new Terminal({
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: terminalTheme(holder.current),
      allowTransparency: true,
    })

    // A look or a mode can change while the shell is open, and a terminal that
    // keeps the colours of the theme it was opened under goes unreadable the
    // moment somebody switches to paper. applyTheme writes these attributes on
    // the root, so that is what is watched.
    const repaint = new MutationObserver(() => {
      if (holder.current) term.options.theme = terminalTheme(holder.current)
    })
    repaint.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme', 'data-look', 'class', 'style'],
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(holder.current)
    fit.fit()

    const base = session.server() || window.location.origin
    const path = workspaceId ? `/api/v1/workspaces/${workspaceId}/terminal` : '/api/v1/terminal'
    const wsUrl = new URL(path, base)
    wsUrl.protocol = wsUrl.protocol === 'https:' ? 'wss:' : 'ws:'
    wsUrl.searchParams.set('rows', String(term.rows))
    wsUrl.searchParams.set('cols', String(term.cols))
    // Browsers cannot set headers on a WebSocket, so the token rides as a
    // subprotocol — the server reads it there when there is no header.
    const token = session.token()
    const ws = token
      ? new WebSocket(wsUrl.toString(), ['bearer', token])
      : new WebSocket(wsUrl.toString())
    socket.current = ws

    ws.onopen = () => {
      setConnected(true)
      term.focus()
    }
    ws.onmessage = (e) => term.write(typeof e.data === 'string' ? e.data : '')
    ws.onclose = () => {
      setConnected(false)
      term.write('\r\n\x1b[2m[disconnected]\x1b[0m\r\n')
    }
    ws.onerror = () => term.write('\r\n\x1b[31m[connection failed]\x1b[0m\r\n')

    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data)
    })

    const onResize = () => {
      fit.fit()
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }))
      }
    }
    window.addEventListener('resize', onResize)

    return () => {
      window.removeEventListener('resize', onResize)
      ws.close()
      term.dispose()
    }
  }, [allowed, workspaceId])

  if (status && !allowed) {
    // The drawer's own panel already said this, in the server's words.
    if (described) return null
    return (
      <div className="page">
        <StageSlot screen="terminal" />
        <PanelTitle>Terminal</PanelTitle>
        <div className="card">
          <p>A terminal is not available here.</p>
          <p className="error">{blockedReason}</p>
        </div>
      </div>
    )
  }

  // Where the other end is, for THIS terminal. `host` is the machine the
  // server runs on, as the account it runs as; anything else is the sandbox
  // gears run in, which has no network and nothing of the server's mounted.
  // The two scopes can differ, so the scope decides which field to read.
  const onHost = (workspaceId ? status?.backend : status?.global_backend) === 'host'

  return (
    <div className={workspaceId ? 'terminal-embedded' : 'page terminal-page'}>
      <div className="row">
        {!workspaceId && <PanelTitle>Terminal</PanelTitle>}
        <span className="muted">
          {connected ? 'connected' : 'connecting…'}
          {described
            ? ''
            : onHost
              ? " · this machine, as this server's user"
              : " · sandboxed, no network, nothing of the server's mounted"}
          {/* "a copy of", not "this workspace's files". The container gets a
              copy and nothing is carried back out, so a file written there is
              gone when the session ends. Saying otherwise is how someone loses
              an hour's work and only finds out afterwards. A host shell is the
              real directory, so this warning would be a lie there. */}
          {workspaceId && !onHost && !described
            ? " · a copy of this workspace's files, not carried back"
            : ''}
        </span>
      </div>
      <div className="terminal-holder" ref={holder} />
    </div>
  )
}
