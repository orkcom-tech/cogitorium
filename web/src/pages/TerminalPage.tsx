import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, type TerminalStatus } from '../api'
import { session } from '../session'

// A shell, running in the same sandbox gears run in — not on the server.
// The server refuses to open one without that sandbox, so what you get here
// cannot read the database or the provider keys in it.
export default function TerminalPage() {
  const [status, setStatus] = useState<TerminalStatus | null>(null)
  const [connected, setConnected] = useState(false)
  const holder = useRef<HTMLDivElement>(null)
  const socket = useRef<WebSocket | null>(null)

  useEffect(() => {
    api.terminal.status().then(setStatus).catch(() => setStatus(null))
  }, [])

  useEffect(() => {
    if (!status?.available || !holder.current) return

    const term = new Terminal({
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: { background: '#00000000' },
      allowTransparency: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(holder.current)
    fit.fit()

    const base = session.server() || window.location.origin
    const wsUrl = new URL('/api/v1/terminal', base)
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
  }, [status])

  if (status && !status.available) {
    return (
      <div className="page">
        <h2>Terminal</h2>
        <div className="card">
          <p>A terminal is not available here.</p>
          <p className="error">{status.reason}</p>
          <p className="hint">
            Gear execution backend: <code>{status.backend}</code>. The terminal runs in that same sandbox — the
            server refuses to open a shell that would hold its own file access.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="page terminal-page">
      <div className="row">
        <h2>Terminal</h2>
        <span className="muted">
          {connected ? 'connected' : 'connecting…'} · sandboxed, no network, nothing of the server's mounted
        </span>
      </div>
      <div className="terminal-holder" ref={holder} />
    </div>
  )
}
